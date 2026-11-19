package sfn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pi-proxy/internal/binlog"
)

type LambdaInvoker func(ctx context.Context, funcName string, payload []byte) (status int, fnErr string, body []byte, err error)

type StateMachine struct {
	ARN          string         `json:"stateMachineArn"`
	Name         string         `json:"name"`
	Definition   ASLDefinition  `json:"-"`
	RawDef       string         `json:"definition"`
	RoleARN      string         `json:"roleArn"`
	CreationDate float64        `json:"creationDate"`
}

type ASLDefinition struct {
	StartAt string              `json:"StartAt"`
	States  map[string]ASLState `json:"States"`
}

type ASLState struct {
	Type       string         `json:"Type"`
	Next       string         `json:"Next"`
	End        bool           `json:"End"`
	Resource   string         `json:"Resource"`
	Parameters map[string]any `json:"Parameters"`
	Result     any            `json:"Result"`
	InputPath  string         `json:"InputPath"`
	ResultPath string         `json:"ResultPath"`
	OutputPath string         `json:"OutputPath"`
	Seconds    int            `json:"Seconds"`
	Error      string         `json:"Error"`
	Cause      string         `json:"Cause"`
	Default    string         `json:"Default"`
	Choices    []ChoiceRule   `json:"Choices"`
	Retry      []RetryRule    `json:"Retry"`
	Catch      []CatchRule    `json:"Catch"`
}

type ChoiceRule struct {
	Variable           string       `json:"Variable"`
	StringEquals       *string      `json:"StringEquals"`
	NumericEquals      *float64     `json:"NumericEquals"`
	NumericGreaterThan *float64     `json:"NumericGreaterThan"`
	NumericLessThan    *float64     `json:"NumericLessThan"`
	BooleanEquals      *bool        `json:"BooleanEquals"`
	IsPresent          *bool        `json:"IsPresent"`
	And                []ChoiceRule `json:"And"`
	Or                 []ChoiceRule `json:"Or"`
	Not                *ChoiceRule  `json:"Not"`
	Next               string       `json:"Next"`
}

type RetryRule struct {
	ErrorEquals     []string `json:"ErrorEquals"`
	IntervalSeconds int      `json:"IntervalSeconds"`
	MaxAttempts     int      `json:"MaxAttempts"`
	BackoffRate     float64  `json:"BackoffRate"`
}

type CatchRule struct {
	ErrorEquals []string `json:"ErrorEquals"`
	ResultPath  string   `json:"ResultPath"`
	Next        string   `json:"Next"`
}

type HistoryEvent struct {
	ID        int64   `json:"id"`
	Timestamp float64 `json:"timestamp"`
	Type      string  `json:"type"`
	State     string  `json:"state,omitempty"`
	Detail    string  `json:"detail,omitempty"`
}

type Execution struct {
	ExecutionARN    string         `json:"executionArn"`
	StateMachineARN string         `json:"stateMachineArn"`
	Name            string         `json:"name"`
	Status          string         `json:"status"`
	Input           string         `json:"input"`
	Output          string         `json:"output,omitempty"`
	Error           string         `json:"error,omitempty"`
	Cause           string         `json:"cause,omitempty"`
	StartDate       float64        `json:"startDate"`
	StopDate        float64        `json:"stopDate,omitempty"`
	History         []HistoryEvent `json:"-"`
}

type Engine struct {
	mu      sync.RWMutex
	sms     map[string]*StateMachine
	execs   map[string]*Execution
	invoker LambdaInvoker
	log     *binlog.Logger
	seq     atomic.Uint64
}

func NewEngine(invoker LambdaInvoker, log *binlog.Logger) *Engine {
	return &Engine{
		sms:     make(map[string]*StateMachine),
		execs:   make(map[string]*Execution),
		invoker: invoker,
		log:     log,
	}
}

func (e *Engine) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	action := strings.TrimPrefix(target, "AWSStepFunctions.")
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))

	switch action {
	case "CreateStateMachine":
		e.handleCreateSM(w, bodyBytes)
	case "DescribeStateMachine":
		e.handleDescribeSM(w, bodyBytes)
	case "ListStateMachines":
		e.handleListSM(w)
	case "DeleteStateMachine":
		e.handleDeleteSM(w, bodyBytes)
	case "StartExecution":
		e.handleStartExec(w, bodyBytes)
	case "DescribeExecution":
		e.handleDescribeExec(w, bodyBytes)
	case "GetExecutionHistory":
		e.handleGetHistory(w, bodyBytes)
	default:
		writeAmzError(w, http.StatusBadRequest, "UnknownOperationException", "Unsupported StepFunctions action: "+action)
	}
}

func (e *Engine) handleCreateSM(w http.ResponseWriter, raw []byte) {
	var req struct {
		Name       string `json:"name"`
		Definition string `json:"definition"`
		RoleARN    string `json:"roleArn"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Name == "" || req.Definition == "" {
		writeAmzError(w, http.StatusBadRequest, "InvalidDefinition", "name and definition are required")
		return
	}
	var def ASLDefinition
	if err := json.Unmarshal([]byte(req.Definition), &def); err != nil || def.StartAt == "" || len(def.States) == 0 {
		writeAmzError(w, http.StatusBadRequest, "InvalidDefinition", "invalid ASL JSON")
		return
	}
	arn := "arn:aws:states:us-east-1:000000000000:stateMachine:" + req.Name
	now := float64(time.Now().UnixMilli()) / 1000.0
	sm := &StateMachine{
		ARN:          arn,
		Name:         req.Name,
		Definition:   def,
		RawDef:       req.Definition,
		RoleARN:      req.RoleARN,
		CreationDate: now,
	}
	e.mu.Lock()
	e.sms[arn] = sm
	e.mu.Unlock()
	if e.log != nil {
		e.log.Write(map[string]any{"event": "sfn_create_sm", "name": req.Name, "arn": arn})
	}
	writeJSON(w, http.StatusOK, map[string]any{"stateMachineArn": arn, "creationDate": now})
}

func (e *Engine) handleDescribeSM(w http.ResponseWriter, raw []byte) {
	var req struct {
		StateMachineARN string `json:"stateMachineArn"`
	}
	_ = json.Unmarshal(raw, &req)
	e.mu.RLock()
	sm, ok := e.sms[req.StateMachineARN]
	e.mu.RUnlock()
	if !ok {
		writeAmzError(w, http.StatusBadRequest, "StateMachineDoesNotExist", "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stateMachineArn": sm.ARN,
		"name":            sm.Name,
		"status":          "ACTIVE",
		"definition":      sm.RawDef,
		"roleArn":         sm.RoleARN,
		"type":            "STANDARD",
		"creationDate":    sm.CreationDate,
	})
}

func (e *Engine) handleListSM(w http.ResponseWriter) {
	e.mu.RLock()
	list := make([]map[string]any, 0, len(e.sms))
	for _, sm := range e.sms {
		list = append(list, map[string]any{
			"stateMachineArn": sm.ARN,
			"name":            sm.Name,
			"type":            "STANDARD",
			"creationDate":    sm.CreationDate,
		})
	}
	e.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"stateMachines": list})
}

func (e *Engine) handleDeleteSM(w http.ResponseWriter, raw []byte) {
	var req struct {
		StateMachineARN string `json:"stateMachineArn"`
	}
	_ = json.Unmarshal(raw, &req)
	e.mu.Lock()
	delete(e.sms, req.StateMachineARN)
	e.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (e *Engine) handleStartExec(w http.ResponseWriter, raw []byte) {
	var req struct {
		StateMachineARN string `json:"stateMachineArn"`
		Name            string `json:"name"`
		Input           string `json:"input"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.StateMachineARN == "" {
		writeAmzError(w, http.StatusBadRequest, "InvalidExecutionInput", "stateMachineArn is required")
		return
	}
	e.mu.RLock()
	sm, ok := e.sms[req.StateMachineARN]
	e.mu.RUnlock()
	if !ok {
		writeAmzError(w, http.StatusBadRequest, "StateMachineDoesNotExist", "state machine not found")
		return
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("exec-%d-%d", time.Now().UnixMilli(), e.seq.Add(1))
	}
	if req.Input == "" {
		req.Input = "{}"
	}
	exARN := fmt.Sprintf("arn:aws:states:us-east-1:000000000000:execution:%s:%s", sm.Name, req.Name)
	now := float64(time.Now().UnixMilli()) / 1000.0
	ex := &Execution{
		ExecutionARN:    exARN,
		StateMachineARN: sm.ARN,
		Name:            req.Name,
		Status:          "RUNNING",
		Input:           req.Input,
		StartDate:       now,
		History:         []HistoryEvent{{ID: 1, Timestamp: now, Type: "ExecutionStarted"}},
	}
	e.mu.Lock()
	e.execs[exARN] = ex
	e.mu.Unlock()

	go e.runExecution(sm, ex)
	writeJSON(w, http.StatusOK, map[string]any{"executionArn": exARN, "startDate": now})
}

func (e *Engine) handleDescribeExec(w http.ResponseWriter, raw []byte) {
	var req struct {
		ExecutionARN string `json:"executionArn"`
	}
	_ = json.Unmarshal(raw, &req)
	e.mu.RLock()
	ex, ok := e.execs[req.ExecutionARN]
	if !ok {
		e.mu.RUnlock()
		writeAmzError(w, http.StatusBadRequest, "ExecutionDoesNotExist", "execution not found")
		return
	}
	cp := *ex
	e.mu.RUnlock()
	writeJSON(w, http.StatusOK, cp)
}

func (e *Engine) handleGetHistory(w http.ResponseWriter, raw []byte) {
	var req struct {
		ExecutionARN string `json:"executionArn"`
	}
	_ = json.Unmarshal(raw, &req)
	e.mu.RLock()
	ex, ok := e.execs[req.ExecutionARN]
	if !ok {
		e.mu.RUnlock()
		writeAmzError(w, http.StatusBadRequest, "ExecutionDoesNotExist", "execution not found")
		return
	}
	events := append([]HistoryEvent(nil), ex.History...)
	e.mu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (e *Engine) addHistory(ex *Execution, typ, state, detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := int64(len(ex.History) + 1)
	ex.History = append(ex.History, HistoryEvent{
		ID:        id,
		Timestamp: float64(time.Now().UnixMilli()) / 1000.0,
		Type:      typ,
		State:     state,
		Detail:    detail,
	})
}

func (e *Engine) finishExec(ex *Execution, status, output, errCode, cause string) {
	now := float64(time.Now().UnixMilli()) / 1000.0
	e.mu.Lock()
	ex.Status = status
	ex.Output = output
	ex.Error = errCode
	ex.Cause = cause
	ex.StopDate = now
	e.mu.Unlock()
	if e.log != nil {
		e.log.Write(map[string]any{"event": "sfn_exec_done", "executionArn": ex.ExecutionARN, "status": status, "error": errCode})
	}
}

func (e *Engine) runExecution(sm *StateMachine, ex *Execution) {
	var data any
	if err := json.Unmarshal([]byte(ex.Input), &data); err != nil {
		e.finishExec(ex, "FAILED", "", "States.Runtime", "Invalid JSON input")
		return
	}

	cur := sm.Definition.StartAt
	for step := 0; step < 100; step++ {
		st, ok := sm.Definition.States[cur]
		if !ok {
			e.finishExec(ex, "FAILED", "", "States.Runtime", "Unknown state: "+cur)
			return
		}
		e.addHistory(ex, st.Type+"StateEntered", cur, "")
		inVal := applyPath(data, st.InputPath)

		switch st.Type {
		case "Pass":
			res := inVal
			if st.Result != nil {
				res = st.Result
			}
			data = applyResultPath(data, st.ResultPath, res)
			data = applyPath(data, st.OutputPath)
			e.addHistory(ex, "PassStateExited", cur, "")
			if st.End || st.Next == "" {
				out, _ := json.Marshal(data)
				e.addHistory(ex, "ExecutionSucceeded", "", "")
				e.finishExec(ex, "SUCCEEDED", string(out), "", "")
				return
			}
			cur = st.Next

		case "Wait":
			sec := st.Seconds
			if sec < 0 {
				sec = 0
			}
			if sec > 30 {
				sec = 30
			}
			time.Sleep(time.Duration(sec) * time.Second)
			e.addHistory(ex, "WaitStateExited", cur, "")
			if st.End || st.Next == "" {
				out, _ := json.Marshal(data)
				e.addHistory(ex, "ExecutionSucceeded", "", "")
				e.finishExec(ex, "SUCCEEDED", string(out), "", "")
				return
			}
			cur = st.Next

		case "Succeed":
			out, _ := json.Marshal(applyPath(data, st.OutputPath))
			e.addHistory(ex, "ExecutionSucceeded", "", "")
			e.finishExec(ex, "SUCCEEDED", string(out), "", "")
			return

		case "Fail":
			errCode := st.Error
			if errCode == "" {
				errCode = "States.Fail"
			}
			e.addHistory(ex, "ExecutionFailed", cur, st.Cause)
			e.finishExec(ex, "FAILED", "", errCode, st.Cause)
			return

		case "Choice":
			next := st.Default
			for _, rule := range st.Choices {
				if evalChoice(rule, inVal) {
					next = rule.Next
					break
				}
			}
			if next == "" {
				e.addHistory(ex, "ExecutionFailed", cur, "No matching choice")
				e.finishExec(ex, "FAILED", "", "States.NoChoiceMatched", "No matching choice rule")
				return
			}
			e.addHistory(ex, "ChoiceStateExited", cur, next)
			cur = next

		case "Task":
			res, errCode, cause := e.execTaskWithRetry(st, inVal)
			if errCode != "" {
				caught := false
				for _, c := range st.Catch {
					if matchesError(c.ErrorEquals, errCode) {
						errObj := map[string]any{"Error": errCode, "Cause": cause}
						data = applyResultPath(data, c.ResultPath, errObj)
						e.addHistory(ex, "TaskFailed", cur, errCode+": "+cause)
						cur = c.Next
						caught = true
						break
					}
				}
				if caught {
					continue
				}
				e.addHistory(ex, "ExecutionFailed", cur, errCode+": "+cause)
				e.finishExec(ex, "FAILED", "", errCode, cause)
				return
			}
			data = applyResultPath(data, st.ResultPath, res)
			data = applyPath(data, st.OutputPath)
			e.addHistory(ex, "TaskSucceeded", cur, "")
			if st.End || st.Next == "" {
				out, _ := json.Marshal(data)
				e.addHistory(ex, "ExecutionSucceeded", "", "")
				e.finishExec(ex, "SUCCEEDED", string(out), "", "")
				return
			}
			cur = st.Next

		default:
			e.finishExec(ex, "FAILED", "", "States.Runtime", "Unsupported state type: "+st.Type)
			return
		}
	}
	e.finishExec(ex, "FAILED", "", "States.Runtime", "Max step limit (100) exceeded")
}

func (e *Engine) execTaskWithRetry(st ASLState, inVal any) (any, string, string) {
	funcName := resolveTaskFunctionName(st)
	if funcName == "" || e.invoker == nil {
		return nil, "States.TaskFailed", "Unsupported task resource: " + st.Resource
	}
	payloadObj := inVal
	if st.Parameters != nil {
		if p, ok := st.Parameters["Payload"]; ok {
			payloadObj = p
		}
	}
	payloadBytes, _ := json.Marshal(payloadObj)

	attempts := 0
	for {
		attempts++
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		status, fnErr, body, err := e.invoker(ctx, funcName, payloadBytes)
		cancel()

		if err == nil && status < 400 && fnErr == "" {
			var out any
			if json.Unmarshal(body, &out) != nil {
				out = string(body)
			}
			return out, "", ""
		}

		errCode := "States.TaskFailed"
		cause := string(body)
		if err != nil {
			cause = err.Error()
		} else {
			var parsed map[string]any
			if json.Unmarshal(body, &parsed) == nil {
				if et, ok := parsed["errorType"].(string); ok && et != "" {
					errCode = et
				}
				if em, ok := parsed["errorMessage"].(string); ok && em != "" {
					cause = em
				}
			}
		}

		retried := false
		for _, r := range st.Retry {
			maxAtt := r.MaxAttempts
			if maxAtt <= 0 {
				maxAtt = 3
			}
			if matchesError(r.ErrorEquals, errCode) && attempts <= maxAtt {
				interval := r.IntervalSeconds
				if interval <= 0 {
					interval = 1
				}
				time.Sleep(time.Duration(interval) * time.Second)
				retried = true
				break
			}
		}
		if !retried {
			return nil, errCode, cause
		}
	}
}

func resolveTaskFunctionName(st ASLState) string {
	if st.Parameters != nil {
		if fn, ok := st.Parameters["FunctionName"].(string); ok && fn != "" {
			if idx := strings.LastIndex(fn, ":"); idx >= 0 {
				return fn[idx+1:]
			}
			return fn
		}
	}
	if strings.Contains(st.Resource, ":function:") {
		parts := strings.Split(st.Resource, ":function:")
		name := parts[len(parts)-1]
		if idx := strings.Index(name, ":"); idx >= 0 {
			name = name[:idx]
		}
		return name
	}
	return ""
}

func matchesError(list []string, errCode string) bool {
	for _, pat := range list {
		if pat == "States.ALL" || pat == errCode {
			return true
		}
	}
	return false
}

func applyPath(data any, p string) any {
	if p == "" || p == "$" {
		return data
	}
	if !strings.HasPrefix(p, "$.") {
		return data
	}
	cur := data
	for _, key := range strings.Split(p[2:], ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func applyResultPath(data any, rp string, res any) any {
	if rp == "" || rp == "$" {
		return res
	}
	if !strings.HasPrefix(rp, "$.") {
		return data
	}
	root, ok := data.(map[string]any)
	if !ok {
		root = map[string]any{}
	}
	cp := make(map[string]any, len(root)+1)
	for k, v := range root {
		cp[k] = v
	}
	keys := strings.Split(rp[2:], ".")
	cur := cp
	for i := 0; i < len(keys)-1; i++ {
		sub, ok := cur[keys[i]].(map[string]any)
		if !ok {
			sub = map[string]any{}
		}
		next := make(map[string]any, len(sub)+1)
		for k, v := range sub {
			next[k] = v
		}
		cur[keys[i]] = next
		cur = next
	}
	cur[keys[len(keys)-1]] = res
	return cp
}

func evalChoice(r ChoiceRule, data any) bool {
	if len(r.And) > 0 {
		for _, sub := range r.And {
			if !evalChoice(sub, data) {
				return false
			}
		}
		return true
	}
	if len(r.Or) > 0 {
		for _, sub := range r.Or {
			if evalChoice(sub, data) {
				return true
			}
		}
		return false
	}
	if r.Not != nil {
		return !evalChoice(*r.Not, data)
	}
	val := applyPath(data, r.Variable)
	if r.IsPresent != nil {
		return (val != nil) == *r.IsPresent
	}
	if r.StringEquals != nil {
		s, ok := val.(string)
		return ok && s == *r.StringEquals
	}
	if r.BooleanEquals != nil {
		b, ok := val.(bool)
		return ok && b == *r.BooleanEquals
	}
	if num, ok := val.(float64); ok {
		if r.NumericEquals != nil {
			return num == *r.NumericEquals
		}
		if r.NumericGreaterThan != nil {
			return num > *r.NumericGreaterThan
		}
		if r.NumericLessThan != nil {
			return num < *r.NumericLessThan
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAmzError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": msg})
}
