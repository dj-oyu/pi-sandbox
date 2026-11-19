package sfn

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStateMachineChoiceTaskCatch(t *testing.T) {
	invoker := func(_ context.Context, funcName string, payload []byte) (int, string, []byte, error) {
		if funcName == "fail-fn" {
			return 200, "Unhandled", []byte(`{"errorType":"PaymentDeclined","errorMessage":"no funds"}`), nil
		}
		return 200, "", []byte(`{"charged":true,"fn":"` + funcName + `"}`), nil
	}
	eng := NewEngine(invoker, nil)

	def := `{
		"StartAt": "Route",
		"States": {
			"Route": {
				"Type": "Choice",
				"Choices": [
					{"Variable": "$.kind", "StringEquals": "bad", "Next": "FailTask"}
				],
				"Default": "OkTask"
			},
			"FailTask": {
				"Type": "Task",
				"Resource": "arn:aws:lambda:us-east-1:000000000000:function:fail-fn",
				"Catch": [{"ErrorEquals": ["PaymentDeclined"], "ResultPath": "$.err", "Next": "Recovered"}],
				"End": true
			},
			"Recovered": {
				"Type": "Pass",
				"Result": {"compensated": true},
				"ResultPath": "$.status",
				"End": true
			},
			"OkTask": {
				"Type": "Task",
				"Resource": "arn:aws:lambda:us-east-1:000000000000:function:ok-fn",
				"End": true
			}
		}
	}`

	createBody, _ := json.Marshal(map[string]string{"name": "order-sm", "definition": def, "roleArn": "arn:r"})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(createBody))
	req.Header.Set("X-Amz-Target", "AWSStepFunctions.CreateStateMachine")
	rw := httptest.NewRecorder()
	eng.HandleHTTP(rw, req)
	if rw.Code != 200 {
		t.Fatalf("create failed: %d %s", rw.Code, rw.Body.String())
	}
	var cr struct{ StateMachineArn string }
	_ = json.Unmarshal(rw.Body.Bytes(), &cr)

	startBody, _ := json.Marshal(map[string]string{"stateMachineArn": cr.StateMachineArn, "input": `{"kind":"bad"}`})
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(startBody))
	req2.Header.Set("X-Amz-Target", "AWSStepFunctions.StartExecution")
	rw2 := httptest.NewRecorder()
	eng.HandleHTTP(rw2, req2)
	var sr struct{ ExecutionArn string }
	_ = json.Unmarshal(rw2.Body.Bytes(), &sr)

	var desc Execution
	for i := 0; i < 40; i++ {
		dBody, _ := json.Marshal(map[string]string{"executionArn": sr.ExecutionArn})
		req3 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(dBody))
		req3.Header.Set("X-Amz-Target", "AWSStepFunctions.DescribeExecution")
		rw3 := httptest.NewRecorder()
		eng.HandleHTTP(rw3, req3)
		_ = json.Unmarshal(rw3.Body.Bytes(), &desc)
		if desc.Status == "SUCCEEDED" || desc.Status == "FAILED" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if desc.Status != "SUCCEEDED" {
		t.Fatalf("expected SUCCEEDED via Catch->Recovered, got %s (%s: %s)", desc.Status, desc.Error, desc.Cause)
	}
}
