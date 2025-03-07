// Copyright 2020 New Relic Corporation. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package newrelic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/rainforestpay/go-agent/v3/internal"
	"github.com/rainforestpay/go-agent/v3/internal/cat"
	"github.com/rainforestpay/go-agent/v3/internal/crossagent"
)

type harvestedTxnTrace struct {
	startTimeMs        float64
	durationToResponse float64
	transactionName    string
	requestURL         string
	traceDetails       struct {
		attributes struct {
			agentAttributes eventAttributes
			userAttributes  eventAttributes
			intrinsics      eventAttributes
		}
	}
	catGUID              string
	forcePersistFlag     bool
	xraySessionID        string
	syntheticsResourceID string
}

func (h *harvestedTxnTrace) UnmarshalJSON(data []byte) error {
	var arr []interface{}

	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}

	if len(arr) != 10 {
		return fmt.Errorf("unexpected number of transaction trace items: %d", len(arr))
	}

	h.startTimeMs = arr[0].(float64)
	h.durationToResponse = arr[1].(float64)
	h.transactionName = arr[2].(string)
	if nil != arr[3] {
		h.requestURL = arr[3].(string)
	}
	// Item 4 -- the trace -- will be dealt with shortly.
	h.catGUID = arr[5].(string)
	// Item 6 intentionally ignored.
	h.forcePersistFlag = arr[7].(bool)
	if arr[8] != nil {
		h.xraySessionID = arr[8].(string)
	}
	h.syntheticsResourceID = arr[9].(string)

	traceDetails := arr[4].([]interface{})
	attributes := traceDetails[4].(map[string]interface{})

	h.traceDetails.attributes.agentAttributes = attributes["agentAttributes"].(map[string]interface{})
	h.traceDetails.attributes.userAttributes = attributes["userAttributes"].(map[string]interface{})
	h.traceDetails.attributes.intrinsics = attributes["intrinsics"].(map[string]interface{})

	return nil
}

func harvestTxnDataTrace(t *txnData) (*harvestedTxnTrace, error) {
	// Since transaction trace JSON is built using string manipulation, we have
	// to do an awkward marshal/unmarshal shuffle to be able to verify the
	// intrinsics.
	ht := harvestTrace{
		txnEvent: t.txnEvent,
		Trace:    t.TxnTrace,
	}
	js, err := ht.MarshalJSON()
	if err != nil {
		return nil, err
	}

	trace := &harvestedTxnTrace{}
	if err := json.Unmarshal(js, trace); err != nil {
		return nil, err
	}

	return trace, nil
}

func TestSynthetics(t *testing.T) {
	var testcases []struct {
		Name     string `json:"name"`
		Settings struct {
			AgentEncodingKey      string `json:"agentEncodingKey"`
			SyntheticsEncodingKey string `json:"syntheticsEncodingKey"`
			TransactionGUID       string `json:"transactionGuid"`
			TrustedAccountIDs     []int  `json:"trustedAccountIds"`
		} `json:"settings"`
		InputHeaderPayload     json.RawMessage   `json:"inputHeaderPayload"`
		InputObfuscatedHeader  map[string]string `json:"inputObfuscatedHeader"`
		OutputTransactionTrace struct {
			Header struct {
				Field9 string `json:"field_9"`
			} `json:"header"`
			ExpectedIntrinsics    map[string]string `json:"expectedIntrinsics"`
			NonExpectedIntrinsics []string          `json:"nonExpectedIntrinsics"`
		} `json:"outputTransactionTrace"`
		OutputTransactionEvent struct {
			ExpectedAttributes    map[string]string `json:"expectedAttributes"`
			NonExpectedAttributes []string          `json:"nonExpectedAttributes"`
		} `json:"outputTransactionEvent"`
		OutputExternalRequestHeader struct {
			ExpectedHeader    map[string]string `json:"expectedHeader"`
			NonExpectedHeader []string          `json:"nonExpectedHeader"`
		} `json:"outputExternalRequestHeader"`
	}

	err := crossagent.ReadJSON("synthetics/synthetics.json", &testcases)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range testcases {
		// Fake enough transaction data to run the test.
		tr := &txnData{
			Name: "txn",
		}

		tr.CrossProcess.Init(false, false, &internal.ConnectReply{
			CrossProcessID:  "1#1",
			TrustedAccounts: make(map[int]struct{}),
			EncodingKey:     tc.Settings.AgentEncodingKey,
		})

		// Set up the trusted accounts.
		for _, account := range tc.Settings.TrustedAccountIDs {
			tr.CrossProcess.TrustedAccounts[account] = struct{}{}
		}

		// Set up the GUID.
		if tc.Settings.TransactionGUID != "" {
			tr.CrossProcess.GUID = tc.Settings.TransactionGUID
		}

		// Parse the input header, ignoring any errors.
		inputHeaders := make(http.Header)
		for k, v := range tc.InputObfuscatedHeader {
			inputHeaders.Add(k, v)
		}

		tr.CrossProcess.handleInboundRequestEncodedSynthetics(inputHeaders.Get(cat.NewRelicSyntheticsName))

		// Get the headers for an external request.
		metadata, err := tr.CrossProcess.CreateCrossProcessMetadata("txn", "app")
		if err != nil {
			t.Fatalf("%s: error creating outbound request headers: %v", tc.Name, err)
		}

		// Verify that the header either exists or doesn't exist, depending on the
		// test case.
		headers := metadataToHTTPHeader(metadata)
		for key, value := range tc.OutputExternalRequestHeader.ExpectedHeader {
			obfuscated := headers.Get(key)
			if obfuscated == "" {
				t.Errorf("%s: expected output header %s not found", tc.Name, key)
			} else if value != obfuscated {
				t.Errorf("%s: expected output header %s mismatch: expected=%s; got=%s", tc.Name, key, value, obfuscated)
			}
		}

		for _, key := range tc.OutputExternalRequestHeader.NonExpectedHeader {
			if value := headers.Get(key); value != "" {
				t.Errorf("%s: output header %s expected to be missing; got %s", tc.Name, key, value)
			}
		}

		// Harvest the trace.
		trace, err := harvestTxnDataTrace(tr)
		if err != nil {
			t.Errorf("%s: error harvesting trace data: %v", tc.Name, err)
		}

		// Check the synthetics resource ID.
		if trace.syntheticsResourceID != tc.OutputTransactionTrace.Header.Field9 {
			t.Errorf("%s: unexpected field 9: expected=%s; got=%s", tc.Name, tc.OutputTransactionTrace.Header.Field9, trace.syntheticsResourceID)
		}

		// Check for expected intrinsics.
		for key, value := range tc.OutputTransactionTrace.ExpectedIntrinsics {
			// First, check if the key exists at all.
			if !trace.traceDetails.attributes.intrinsics.has(key) {
				t.Fatalf("%s: missing intrinsic %s", tc.Name, key)
			}

			// Everything we're looking for is a string, so we can be a little lazy
			// here.
			if err := trace.traceDetails.attributes.intrinsics.isString(key, value); err != nil {
				t.Errorf("%s: %v", tc.Name, err)
			}
		}

		// Now we verify that the unexpected intrinsics didn't miraculously appear.
		for _, key := range tc.OutputTransactionTrace.NonExpectedIntrinsics {
			if trace.traceDetails.attributes.intrinsics.has(key) {
				t.Errorf("%s: expected intrinsic %s to be missing; instead, got value %v", tc.Name, key,
					trace.traceDetails.attributes.intrinsics[key])
			}
		}

		// Harvest the event.
		event, err := harvestTxnDataEvent(tr)
		if err != nil {
			t.Errorf("%s: error harvesting event data: %v", tc.Name, err)
		}

		// Now we have the event, let's look for the expected intrinsics.
		for key, value := range tc.OutputTransactionEvent.ExpectedAttributes {
			// First, check if the key exists at all.
			if !event.intrinsics.has(key) {
				t.Fatalf("%s: missing intrinsic %s", tc.Name, key)
			}

			// Everything we're looking for is a string, so we can be a little lazy
			// here.
			if err := event.intrinsics.isString(key, value); err != nil {
				t.Errorf("%s: %v", tc.Name, err)
			}
		}

		// Now we verify that the unexpected intrinsics didn't miraculously appear.
		for _, key := range tc.OutputTransactionEvent.NonExpectedAttributes {
			if event.intrinsics.has(key) {
				t.Errorf("%s: expected intrinsic %s to be missing; instead, got value %v", tc.Name, key, event.intrinsics[key])
			}
		}
	}
}
