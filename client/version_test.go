// Regression tests added for OpenCode V1/V2 support.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

const serverInfoV2 = `{"version":"2.1.1","pid":12,"urls":[],"paths":{"tmp":"/tmp/test"}}`
const locationV2 = `{"directory":"/work/a","project":{"id":"prj_a","directory":"/work/a","canonical":"/work/a"}}`
const sessionV2 = `{"id":"ses_test","projectID":"prj_a","location":{"directory":"/work/a"}}`

func TestStructuralDetection(t *testing.T) {
	for _,tt:=range []struct {name,health,info,want string; code int; infoCalls int}{
		{"v1",`{"healthy":true,"version":"1.2.27"}`,serverInfoV2,"v1",200,0},
		{"v2 HTML fallback",`<!doctype html><html></html>`,serverInfoV2,"v2",200,1},
		{"v2 missing route",`not found`,serverInfoV2,"v2",404,1},
		{"auth must not fallback",`denied`,serverInfoV2,"",401,0},
		{"server failure must not fallback",`failed`,serverInfoV2,"",503,0},
		{"wrong JSON",`{"ok":true}`,serverInfoV2,"",200,0},
		{"unhealthy",`{"healthy":false,"version":"1.2.27"}`,serverInfoV2,"",200,0},
		{"invalid V2 identity",`<html/>`,`{"version":"2.1.1"}`,"",200,1},
		{"HTML is not V2",`<html/>`,`<html/>`,"",200,1},
	} {
		t.Run(tt.name,func(t *testing.T){
			calls:=0
			s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				if r.Method!="GET" { t.Errorf("probe mutated upstream: %s",r.Method) }
				if r.URL.Path=="/global/health" {w.WriteHeader(tt.code);_,_=w.Write([]byte(tt.health));return}
				if r.URL.Path=="/api/info" {calls++;_,_=w.Write([]byte(tt.info));return}
				http.NotFound(w,r)
			}));defer s.Close()
			c:=NewWithOptions(s.URL,"","",s.Client(),Options{})
			_,err:=c.useV2(context.Background())
			if (err==nil)!=(tt.want!="") || c.version!=tt.want || calls!=tt.infoCalls {t.Fatalf("version=%q err=%v probes=%d",c.version,err,calls)}
		})
	}
}

func TestDetectionConnectionFailure(t *testing.T) {
	c:=NewWithOptions("http://127.0.0.1:1","","",&http.Client{Timeout:time.Second},Options{})
	_,err:=c.useV2(context.Background())
	if !errors.Is(err,ErrUnavailable) || c.version!="" {t.Fatalf("%v",err)}
}

func v2Fixture(t *testing.T, extra http.HandlerFunc) (*Client,*httptest.Server) {
	t.Helper()
	s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		u,p,ok:=r.BasicAuth();if !ok||u!="opencode"||p!="test-password" {w.WriteHeader(401);return}
		w.Header().Set("Content-Type","application/json")
		switch r.URL.Path {
		case "/api/info":_,_=w.Write([]byte(serverInfoV2))
		case "/api/location":
			if r.URL.Query().Get("location[directory]")!="/work/a" {t.Errorf("missing deepObject location: %s",r.URL)}
			_,_=w.Write([]byte(locationV2))
		default:extra(w,r)
		}
	}));
	return NewWithOptions(s.URL,"opencode","test-password",s.Client(),Options{APIVersion:"v2",Directory:"/work/a"}),s
}

func TestV2AskReplyRunAndReadOperations(t *testing.T) {
	var prompts int
	var switches int
	c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){
		switch r.URL.Path {
		case "/api/session":
			if r.Method=="POST" {
				var in struct { Location struct {Directory string `json:"directory"`} `json:"location"` };_ = json.NewDecoder(r.Body).Decode(&in)
				if in.Location.Directory!="/work/a" {t.Errorf("session location=%q",in.Location.Directory)}
				_,_=w.Write([]byte(`{"data":`+sessionV2+`}`))
			} else { _,_=w.Write([]byte(`{"data":[`+sessionV2+`],"cursor":{}}`)) }
		case "/api/session/ses_test":_,_=w.Write([]byte(`{"data":`+sessionV2+`}`))
		case "/api/session/ses_test/model":
			var in struct { Model struct { ID string `json:"id"`; ProviderID string `json:"providerID"` } `json:"model"` };_=json.NewDecoder(r.Body).Decode(&in)
			if in.Model.ID!="fixed" || in.Model.ProviderID!="mock" {t.Errorf("incorrect V2 model: %#v",in)}
			switches++;w.WriteHeader(204)
		case "/api/session/ses_test/agent":switches++;w.WriteHeader(204)
		case "/api/session/ses_test/prompt":
			var in map[string]any;_=json.NewDecoder(r.Body).Decode(&in)
			if in["text"]==nil||in["parts"]!=nil {t.Errorf("incorrect V2 prompt: %#v",in)}
			prompts++;_,_=w.Write([]byte(`{"data":{"id":"msg_input"}}`))
		case "/api/session/ses_test/message":
			// The wire order is descending. Include reasoning and tool text
			// that must never become the user-facing answer.
			_,_=w.Write([]byte(`{"data":[{"id":"msg_idle","type":"idle","outcome":"succeeded"},{"id":"msg_answer","type":"assistant","content":[{"type":"reasoning","text":"private"},{"type":"tool","text":"tool output"},{"type":"text","text":"answer"}]},{"id":"msg_input","type":"user","text":"test"}],"cursor":{}}`))
		case "/api/session/ses_test/permission","/api/session/ses_test/form":_,_=w.Write([]byte(`{"data":[]}`))
		case "/api/session/active":_,_=w.Write([]byte(`{"data":{}}`))
		case "/api/mcp":_,_=w.Write([]byte(`{"location":{"directory":"/work/a"},"data":[]}`))
		case "/api/provider":_,_=w.Write([]byte(`{"data":[{"id":"mock"}]}`))
		case "/api/model":_,_=w.Write([]byte(`{"data":[{"id":"fixed","providerID":"mock","enabled":true}]}`))
		default:t.Errorf("unexpected route %s %s",r.Method,r.URL);http.NotFound(w,r)
		}
	});defer s.Close()
	ctx:=context.Background()
	ask:=bridgetypes.AskRequest{Prompt:"test",ProviderID:"mock",ModelID:"fixed",Agent:"build"}
	answer,err:=c.Ask(ctx,ask);if err!=nil||answer.Message!="answer"||answer.SessionID!="ses_test" {t.Fatalf("%#v %v",answer,err)}
	answer,err=c.SendMessage(ctx,bridgetypes.ReplyRequest{SessionID:"ses_test",Prompt:"follow-up"});if err!=nil||answer.Message!="answer" {t.Fatalf("%#v %v",answer,err)}
	run,err:=c.Run(ctx,ask);if err!=nil||run.Status!="completed"||run.Message!="answer" {t.Fatalf("%#v %v",run,err)}
	for _,call:=range []func()(json.RawMessage,error){func()(json.RawMessage,error){return c.Health(ctx)},func()(json.RawMessage,error){return c.Project(ctx)},func()(json.RawMessage,error){return c.Sessions(ctx)},func()(json.RawMessage,error){return c.Conversation(ctx,"ses_test",10)},func()(json.RawMessage,error){return c.SessionStatuses(ctx)},func()(json.RawMessage,error){return c.MCPServers(ctx)},func()(json.RawMessage,error){return c.Providers(ctx)}} {
		if _,err:=call();err!=nil {t.Fatal(err)}
	}
	if prompts!=3||switches!=4 {t.Fatalf("prompts=%d switches=%d",prompts,switches)}
	if _,err:=c.Todos(ctx,"ses_test");err==nil {t.Fatal("unsupported todos succeeded")}
}

func TestV2RejectsForeignSessionBeforeMutation(t *testing.T) {
	mutations:=0
	c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){
		if r.Method!="GET" {mutations++}
		_,_=w.Write([]byte(`{"data":{"id":"ses_test","projectID":"prj_b","location":{"directory":"/work/b"}}}`))
	});defer s.Close()
	_,err:=c.SendMessage(context.Background(),bridgetypes.ReplyRequest{SessionID:"ses_test",Prompt:"test"})
	if err==nil||!strings.Contains(err.Error(),"belongs to directory")||mutations!=0 {t.Fatalf("%v mutations=%d",err,mutations)}
}

func TestV2DoesNotReplayFailedMutation(t *testing.T) {
	mutations:=0
	c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){mutations++;w.WriteHeader(500)});defer s.Close()
	_,_,err:=c.CreateSession(context.Background(),"test")
	if err==nil||mutations!=1 {t.Fatalf("%v mutations=%d",err,mutations)}
}

func TestV2WaitRejectsPendingErrorAndEmptyAnswers(t *testing.T) {
	for _,tt:=range []struct{name,permission,form,messages,want string}{
		{"approval",`[{}]`,`[]`,`[]`,"approval_required"},
		{"question",`[]`,`[{}]`,`[]`,"question_required"},
		{"failed",`[]`,`[]`,`[{"id":"idle","type":"idle","outcome":"failed"},{"id":"input","type":"user"}]`,"execution"},
		{"empty",`[]`,`[]`,`[{"id":"idle","type":"idle","outcome":"succeeded"},{"id":"a","type":"assistant","content":[{"type":"reasoning","text":"hidden"}]},{"id":"input","type":"user"}]`,"without an assistant"},
		{"provider error",`[]`,`[]`,`[{"id":"a","type":"assistant","error":{"message":"bad provider"}},{"id":"input","type":"user"}]`,"provider failed"},
	} {t.Run(tt.name,func(t *testing.T){
		c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){
			switch {case strings.HasSuffix(r.URL.Path,"/permission"):_,_=w.Write([]byte(`{"data":`+tt.permission+`}`));case strings.HasSuffix(r.URL.Path,"/form"):_,_=w.Write([]byte(`{"data":`+tt.form+`}`));case strings.HasSuffix(r.URL.Path,"/message"):_,_=w.Write([]byte(`{"data":`+tt.messages+`,"cursor":{}}`));default:http.NotFound(w,r)}
		});defer s.Close()
		_,err:=c.waitV2(context.Background(),"ses_test","input");if err==nil||!strings.Contains(err.Error(),tt.want) {t.Fatalf("%v",err)}
	})}
}

func TestV2TimeoutInterruptsWithoutReturningOldAnswer(t *testing.T) {
	var interrupted atomic.Bool
	c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){
		switch {case r.URL.Path=="/api/session/ses_test":_,_=w.Write([]byte(`{"data":`+sessionV2+`}`));case strings.HasSuffix(r.URL.Path,"/permission"),strings.HasSuffix(r.URL.Path,"/form"):_,_=w.Write([]byte(`{"data":[]}`));case strings.HasSuffix(r.URL.Path,"/message"):_,_=w.Write([]byte(`{"data":[{"id":"old","type":"assistant","content":[{"type":"text","text":"stale"}]}],"cursor":{}}`));case strings.HasSuffix(r.URL.Path,"/interrupt"):interrupted.Store(true);_,_=w.Write([]byte(`{"interrupted":true}`));default:http.NotFound(w,r)}
	});defer s.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),25*time.Millisecond);defer cancel()
	_,err:=c.waitV2(ctx,"ses_test","new_input")
	if !errors.Is(err,context.DeadlineExceeded)||!interrupted.Load() {t.Fatalf("%v interrupted=%v",err,interrupted.Load())}
	interrupted.Store(false)
	cancelled,stop:=context.WithCancel(context.Background())
	stop()
	_,err=c.waitV2(cancelled,"ses_test","new_input")
	if !errors.Is(err,context.Canceled)||!interrupted.Load() {t.Fatalf("%v interrupted=%v",err,interrupted.Load())}
}

func TestV2TargetedStatusDoesNotAssumeUnlistedSessionIsIdle(t *testing.T) {
	c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){
		switch r.URL.Path {
		case "/api/session/ses_test":_,_=w.Write([]byte(`{"data":{"id":"ses_test","projectID":"prj_a","location":{"directory":"/work/a"},"outcome":"failed"}}`))
		case "/api/session/ses_test/permission","/api/session/ses_test/form":_,_=w.Write([]byte(`{"data":[]}`))
		case "/api/session/active":_,_=w.Write([]byte(`{"data":{}}`))
		default:t.Errorf("unexpected lookup %s",r.URL);http.NotFound(w,r)
		}
	});defer s.Close()
	state,err:=ReadSessionStatus(context.Background(),c,"ses_test")
	if err!=nil||findString(state,"type")!="failed" {t.Fatalf("%s %v",state,err)}
}

func TestV2RejectsInvalidEnvelope(t *testing.T) {
	for _,body:=range []string{`{}`,`{"data":null}`,`[]`,`<html/>`} {
		c,s:=v2Fixture(t,func(w http.ResponseWriter,r *http.Request){_,_=w.Write([]byte(body))})
		_,err:=c.v2Data(context.Background(),http.MethodGet,"/api/provider",nil)
		s.Close()
		if err==nil {t.Fatalf("accepted %s",body)}
	}
}

func TestV1TextExcludesThoughtsAndToolOutput(t *testing.T) {
	raw:=json.RawMessage(`{"parts":[{"type":"reasoning","text":"private"},{"type":"tool","text":"output"},{"type":"text","text":"answer"}]}`)
	if got:=extractMessage(raw);got!="answer" {t.Fatalf("%q",got)}
	if got:=extractMessage(json.RawMessage(`{"parts":[{"type":"reasoning","text":"private"}]}`));got!="" {t.Fatalf("%q",got)}
}
