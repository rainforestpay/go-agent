// Copyright 2022 New Relic Corporation. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package newrelic

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
)

//
// defaultAgentProjectRoot is the default filename pattern which is at
// the root of the agent's import path. This is used to identify functions
// on the call stack which are assumed to belong to the agent rather than
// the instrumented application's code.
//
const defaultAgentProjectRoot = "github.com/newrelic/go-agent/"

//
// CodeLocation marks the location of a line of source code for later reference.
//
type CodeLocation struct {
	// LineNo is the line number within the source file.
	LineNo int
	// Function is the function name (note that this may be auto-generated by Go
	// for function literals and the like). This is the fully-qualified name, which
	// includes the package name and other information to unambiguously identify
	// the function.
	Function string
	// FilePath is the absolute pathname on disk of the source file referred to.
	FilePath string
}

type traceOptSet struct {
	LocationOverride *CodeLocation
	SuppressCLM      bool
	DemandCLM        bool
	IgnoredPrefixes  []string
	PathPrefixes     []string
}

//
// TraceOption values provide optional parameters to transactions.
//
type TraceOption func(*traceOptSet)

//
// WithCodeLocation adds an explicit CodeLocation value
// to report for the Code Level Metrics attached to a trace.
// This is probably a value previously obtained by calling
// ThisCodeLocation().
//
func WithCodeLocation(loc *CodeLocation) TraceOption {
	return func(o *traceOptSet) {
		o.LocationOverride = loc
	}
}

//
// WithIgnoredPrefix indicates that the code location reported
// for Code Level Metrics should be the first function in the
// call stack that does not begin with the given string (or any of the given strings if more than one are given). This
// string is matched against the entire fully-qualified function
// name, which includes the name of the package the function
// comes from. By default, the Go Agent tries to take the first
// function on the call stack that doesn't seem to be internal to
// the agent itself, but you can control this behavior using
// this option.
//
// If all functions in the call stack begin with this prefix,
// the outermost one will be used anyway, since we didn't find
// anything better on the way to the bottom of the stack.
//
// If no prefix strings are passed here, the configured defaults will be used.
//
func WithIgnoredPrefix(prefix ...string) TraceOption {
	return func(o *traceOptSet) {
		o.IgnoredPrefixes = prefix
	}
}

//
// WithPathPrefix overrides the list of source code path prefixes
// used to trim source file pathnames, providing a new set of one
// or more path prefixes to use for this trace only.
// If no strings are given, the configured defaults will be used.
//
func WithPathPrefix(prefix ...string) TraceOption {
	return func(o *traceOptSet) {
		o.PathPrefixes = prefix
	}
}

//
// WithoutCodeLevelMetrics suppresses the collection and reporting
// of Code Level Metrics for this trace. This helps avoid the overhead
// of collecting that information if it's not needed for certain traces.
//
func WithoutCodeLevelMetrics() TraceOption {
	return func(o *traceOptSet) {
		o.SuppressCLM = true
	}
}

//
// WithCodeLevelMetrics includes this trace in code level metrics even if
// it would otherwise not be (for example, if it would be out of the configured
// scope setting). This will never cause code level metrics to be reported if
// CLM were explicitly disabled (e.g. by CLM being globally off or WithoutCodeLevelMetrics
// being present in the options for this trace).
//
func WithCodeLevelMetrics() TraceOption {
	return func(o *traceOptSet) {
		o.DemandCLM = true
	}
}

//
// WithThisCodeLocation is equivalent to calling WithCodeLocation, referring
// to the point in the code where the WithThisCodeLocation call is being made.
// This can be helpful, for example, when the actual code invocation which starts
// a transaction or other kind of trace is originating from a framework or other
// centralized location, but you want to report this point in your application
// for the Code Level Metrics associated with this trace.
//
func WithThisCodeLocation() TraceOption {
	return WithCodeLocation(ThisCodeLocation(1))
}

//
// FunctionLocation is like ThisCodeLocation, but takes as its parameter
// a function value. It will report the code-level metrics information for
// that function if that is possible to do. It returns an error if it
// was not possible to get a code location from the parameter passed to it.
//
func FunctionLocation(function interface{}) (*CodeLocation, error) {
	if function == nil {
		return nil, errors.New("nil function passed to FunctionLocation")
	}

	v := reflect.ValueOf(function)
	if !v.IsValid() || v.Kind() != reflect.Func {
		return nil, errors.New("value passed to FunctionLocation is not a function")
	}

	if fInfo := runtime.FuncForPC(v.Pointer()); fInfo != nil {
		var loc CodeLocation

		loc.FilePath, loc.LineNo = fInfo.FileLine(fInfo.Entry())
		loc.Function = fInfo.Name()
		return &loc, nil
	}

	return nil, errors.New("could not find code location for function")
}

//
// WithFunctionLocation is like WithThisCodeLocation, but uses the
// function value passed as the location to report. Unlike FunctionLocation,
// this does not report errors explicitly. If it is unable to use the
// value passed to find a code location, it will do nothing.
//
func WithFunctionLocation(function interface{}) TraceOption {
	return func(o *traceOptSet) {
		loc, err := FunctionLocation(function)
		if err == nil {
			o.LocationOverride = loc
		}
	}
}

//
// WithDefaultFunctionLocation is like WithFunctionLocation but will only
// evaluate the location of the function if nothing that came before it
// set a code location first. This is useful, for example, if you want to
// provide a default code location value to be used but not pay the overhead
// of resolving that location until it's clear that you will need to. This
// should appear at the end of a TraceOption list (or at least before any
// other options that want to specify the code location).
//
func WithDefaultFunctionLocation(function interface{}) TraceOption {
	return func(o *traceOptSet) {
		if o.LocationOverride == nil {
			WithFunctionLocation(function)(o)
		}
	}
}

//
// withPreparedOptions copies the option settings from a structure
// which was already set up (probably by executing a set of TraceOption
// functions already).
//
func withPreparedOptions(newOptions *traceOptSet) TraceOption {
	return func(o *traceOptSet) {
		if newOptions != nil {
			if newOptions.LocationOverride != nil {
				o.LocationOverride = newOptions.LocationOverride
			}
			o.SuppressCLM = newOptions.SuppressCLM
			o.DemandCLM = newOptions.DemandCLM
			if newOptions.IgnoredPrefixes != nil {
				o.IgnoredPrefixes = newOptions.IgnoredPrefixes
			}
			if newOptions.PathPrefixes != nil {
				o.PathPrefixes = newOptions.PathPrefixes
			}
		}
	}
}

//
// ThisCodeLocation returns a CodeLocation value referring to
// the place in your code that it was invoked.
//
// With no arguments (or if passed a 0 value), it returns the location
// of its own caller. However, you may adjust this by passing the number
// of function calls to skip. For example, ThisCodeLocation(1) will return
// the CodeLocation of the place the current function was called from
// (i.e., the caller of the caller of ThisCodeLocation).
//
func ThisCodeLocation(skipLevels ...int) *CodeLocation {
	var loc CodeLocation
	skip := 2
	if len(skipLevels) > 0 {
		skip += skipLevels[0]
	}

	pcs := make([]uintptr, 10)
	depth := runtime.Callers(skip, pcs)
	if depth > 0 {
		frames := runtime.CallersFrames(pcs[:1])
		frame, _ := frames.Next()
		loc.LineNo = frame.Line
		loc.Function = frame.Function
		loc.FilePath = frame.File
	}
	return &loc
}

func removeCodeLevelMetrics(remAttr func(string)) {
	remAttr(AttributeCodeLineno)
	remAttr(AttributeCodeNamespace)
	remAttr(AttributeCodeFilepath)
	remAttr(AttributeCodeFunction)
}

//
// Evaluate a set of TraceOptions, returning a pointer to a new traceOptSet struct
// initialized from those options. To avoid any unnecessary performance penalties,
// if we encounter an option that suppresses CLM collection, we stop without evaluating
// anything further.
//
func resolveCLMTraceOptions(options []TraceOption) *traceOptSet {
	optSet := traceOptSet{}
	for _, o := range options {
		o(&optSet)
		if optSet.SuppressCLM {
			break
		}
	}
	return &optSet
}

func reportCodeLevelMetrics(tOpts traceOptSet, run *appRun, setAttr func(string, string, interface{})) {
	var location CodeLocation

	if tOpts.LocationOverride != nil {
		location = *tOpts.LocationOverride
	} else {
		pcs := make([]uintptr, 10)
		depth := runtime.Callers(2, pcs)
		if depth > 0 {
			frames := runtime.CallersFrames(pcs[:depth])
			moreToRead := true
			var frame runtime.Frame

			if tOpts.IgnoredPrefixes == nil {
				tOpts.IgnoredPrefixes = run.Config.CodeLevelMetrics.IgnoredPrefixes
				// for backward compatibility, add the singleton IgnoredPrefix if there is one
				if run.Config.CodeLevelMetrics.IgnoredPrefix != "" {
					tOpts.IgnoredPrefixes = append(tOpts.IgnoredPrefixes, run.Config.CodeLevelMetrics.IgnoredPrefix)
				}
				if tOpts.IgnoredPrefixes == nil {
					tOpts.IgnoredPrefixes = append(tOpts.IgnoredPrefixes, defaultAgentProjectRoot)
				}
			}

			// skip out to first non-agent frame, unless that IS the top-most frame
			for moreToRead {
				frame, moreToRead = frames.Next()
				if func() bool {
					for _, eachPrefix := range tOpts.IgnoredPrefixes {
						if strings.HasPrefix(frame.Function, eachPrefix) {
							return false
						}
					}
					return true
				}() {
					break
				}
			}

			location.FilePath = frame.File
			location.Function = frame.Function
			location.LineNo = frame.Line
		}
	}

	if tOpts.PathPrefixes == nil {
		tOpts.PathPrefixes = run.Config.CodeLevelMetrics.PathPrefixes
		// bring in a value still lingering in the deprecated PathPrefix field if the user put one there on their own
		if run.Config.CodeLevelMetrics.PathPrefix != "" {
			tOpts.PathPrefixes = append(tOpts.PathPrefixes, run.Config.CodeLevelMetrics.PathPrefix)
		}
	}

	// scan for any requested suppression of leading parts of file pathnames
	if tOpts.PathPrefixes != nil {
		for _, prefix := range tOpts.PathPrefixes {
			if pi := strings.Index(location.FilePath, prefix); pi >= 0 {
				location.FilePath = location.FilePath[pi:]
				break
			}
		}
	}

	ns := strings.LastIndex(location.Function, ".")
	function := location.Function
	namespace := ""

	if ns >= 0 {
		namespace = location.Function[:ns]
		function = location.Function[ns+1:]
	}

	setAttr(AttributeCodeLineno, "", location.LineNo)
	setAttr(AttributeCodeNamespace, namespace, nil)
	setAttr(AttributeCodeFilepath, location.FilePath, nil)
	setAttr(AttributeCodeFunction, function, nil)
}
