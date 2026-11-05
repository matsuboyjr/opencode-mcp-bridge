// Regression tests added for configured V1 compatibility.
package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

func TestConfiguredV1AskReplyAndRun(t *testing.T) {
	for _,pending:=range []string{"", "approval_required", "question_required"} {
		t.Run("state="+pending,func(t *testing.T){
			prompts:=0
			s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				w.Header().Set("Content-Type","application/json")
				if r.URL.Path!="/global/health" && r.URL.Query().Get("directory")!="/work/a" {t.Errorf("directory not propagated: %s",r.URL)}
				switch r.URL.Path {
				case "/global/health":_,_=w.Write([]byte(`{"healthy":true,"version":"1.2.27"}`))
				case "/session":_,_=w.Write([]byte(`{"id":"ses_test","directory":"/work/a"}`))
				case "/session/ses_test":_,_=w.Write([]byte(`{"id":"ses_test","directory":"/work/a"}`))
				case "/session/ses_test/prompt_async":prompts++;w.WriteHeader(204)
				case "/session/ses_test/message":
					if prompts==0 {_,_=w.Write([]byte(`[]`));return}
					_,_=fmt.Fprintf(w,`[{"info":{"id":"msg_answer_%d","role":"assistant","time":{"completed":1}},"parts":[{"type":"reasoning","text":"private"},{"type":"text","text":"answer"}]}]`,prompts)
				case "/session/status":_,_=w.Write([]byte(`{"ses_test":{"type":"idle"}}`))
				case "/permission":
					if pending=="approval_required" {_,_=w.Write([]byte(`[{"sessionID":"ses_test"}]`))} else {_,_=w.Write([]byte(`[]`))}
				case "/question":
					if pending=="question_required" {_,_=w.Write([]byte(`[{"sessionID":"ses_test"}]`))} else {_,_=w.Write([]byte(`[]`))}
				default:http.NotFound(w,r)
				}
			}));defer s.Close()
			c:=NewWithOptions(s.URL,"","",s.Client(),Options{Directory:"/work/a"})
			ctx,cancel:=context.WithTimeout(context.Background(),3*time.Second);defer cancel()
			result,err:=c.Ask(ctx,bridgetypes.AskRequest{Prompt:"test",ProviderID:"mock",ModelID:"fixed",Agent:"build"})
			if pending!="" {if err==nil||!strings.Contains(err.Error(),pending) {t.Fatalf("%#v %v",result,err)};return}
			if err!=nil||result.Message!="answer"||prompts!=1 {t.Fatalf("%#v %v prompts=%d",result,err,prompts)}
			reply,err:=c.SendMessage(ctx,bridgetypes.ReplyRequest{SessionID:result.SessionID,Prompt:"follow-up"})
			if err!=nil||reply.Message!="answer"||prompts!=2 {t.Fatalf("%#v %v",reply,err)}
			// A new task uses the same asynchronous V1 path.
			run,err:=c.Run(ctx,bridgetypes.AskRequest{Prompt:"test"})
			if err!=nil||run.Status!="completed"||prompts!=3 {t.Fatalf("%#v %v",run,err)}
		})
	}
}

func TestV1AnswerRejectsStaleAndFailedAssistant(t *testing.T) {
	previous:=map[string]bool{"old":true}
	stale:=[]byte(`[{"info":{"id":"old","role":"assistant","time":{"completed":1}},"parts":[{"type":"text","text":"stale"}]}]`)
	if text,err:=v1Answer(stale,previous,true);text!=""||err!=nil {t.Fatalf("%q %v",text,err)}
	failed:=[]byte(`[{"info":{"id":"new","role":"assistant","error":{"message":"provider unavailable"}},"parts":[{"type":"text","text":"partial"}]}]`)
	if _,err:=v1Answer(failed,nil,true);err==nil {t.Fatal("failed message returned success")}
	incomplete:=[]byte(`[{"info":{"id":"new","role":"assistant","time":{}},"parts":[{"type":"text","text":"partial"}]}]`)
	if text,err:=v1Answer(incomplete,nil,true);text!=""||err!=nil {t.Fatalf("%q %v",text,err)}
}

func TestConfiguredV1RejectsForeignSession(t *testing.T) {
	mutations:=0
	s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.Method!="GET" {mutations++}
		if r.URL.Path=="/global/health" {_,_=w.Write([]byte(`{"healthy":true,"version":"1.2.27"}`));return}
		_,_=w.Write([]byte(`{"id":"ses_foreign","directory":"/work/b"}`))
	}));defer s.Close()
	c:=NewWithOptions(s.URL,"","",s.Client(),Options{APIVersion:"v1",Directory:"/work/a"})
	_,err:=c.SendMessage(context.Background(),bridgetypes.ReplyRequest{SessionID:"ses_foreign",Prompt:"must not send"})
	if err==nil||mutations!=0 {t.Fatalf("%v mutations=%d",err,mutations)}
}
