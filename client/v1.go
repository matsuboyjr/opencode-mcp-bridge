// Modified V1 behavior: workspace ownership, pending requests and bounded waits.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

func (c *Client) guardV1(ctx context.Context, path string) error {
	u, err := url.Parse(path); if err != nil { return err }
	segments := strings.Split(strings.Trim(u.Path,"/"),"/")
	if len(segments)<2 || segments[0]!="session" || segments[1]=="status" { return nil }
	directory,err:=c.v1Directory(ctx); if err!=nil { return err }
	raw,err:=c.request(ctx,http.MethodGet,c.v1Path("/session/"+url.PathEscape(segments[1])),nil); if err!=nil { return err }
	if findString(raw,"directory")!=directory { return fmt.Errorf("session belongs to a different directory than %q",directory) }
	return nil
}

func (c *Client) v1Directory(ctx context.Context) (string,error) {
	if c.options.Directory!="" { return c.options.Directory,nil }
	raw,err:=c.request(ctx,http.MethodGet,"/path",nil);if err!=nil { return "",err }
	directory:=findString(raw,"directory");if directory=="" {return "",errors.New("V1 did not report its default directory")};return directory,nil
}

func (c *Client) v1Sessions(ctx context.Context) (json.RawMessage,error) {
	directory,err:=c.v1Directory(ctx);if err!=nil {return nil,err}
	// /session is project-scoped and cannot include nested Git projects.
	// The global endpoint's timestamp cursor cannot disambiguate equal
	// timestamps, so grow its uncapped limit until the complete list fits.
	for limit := 100; ; limit *= 2 {
		query := url.Values{"limit": {fmt.Sprint(limit)}, "archived": {"true"}}
		raw,err:=c.request(ctx,http.MethodGet,"/experimental/session?"+query.Encode(),nil);if err!=nil {return nil,err}
		var sessions []json.RawMessage
		if json.Unmarshal(raw,&sessions)!=nil||sessions==nil {return nil,errors.New("invalid V1 global sessions list")}
		if len(sessions) >= limit {
			if limit > int(^uint(0)>>1)/2 { return nil,errors.New("V1 global sessions list exceeds supported size") }
			continue
		}
		result:=[]json.RawMessage{}
		for _,session:=range sessions {
			location:=findString(session,"directory")
			if findString(session,"id")=="" || location=="" { return nil,errors.New("invalid V1 global session") }
			if directoryWithin(directory,location) {result=append(result,session)}
		}
		return encode(result),nil
	}
}

func (c *Client) v1PendingStatuses(ctx context.Context, statuses json.RawMessage) (json.RawMessage,error) {
	var states map[string]json.RawMessage
	if json.Unmarshal(statuses,&states)!=nil || states==nil { return nil,errors.New("invalid V1 session statuses") }
	for _,p:=range []struct{path,state string}{{"/permission","approval_required"},{"/question","question_required"}} {
		raw,err:=c.request(ctx,http.MethodGet,c.v1Path(p.path),nil)
		if err!=nil { return nil,err }
		var pending []struct { SessionID string `json:"sessionID"` }
		if json.Unmarshal(raw,&pending)!=nil || pending==nil { return nil,errors.New("invalid V1 pending requests") }
		for _,request:=range pending { states[request.SessionID]=encode(map[string]string{"type":p.state}) }
	}
	return encode(states),nil
}

func (c *Client) sendV1AndWait(ctx context.Context, req bridgetypes.ReplyRequest) (bridgetypes.MessageResponse,error) {
	previous,err:=c.Conversation(ctx,req.SessionID,1000); if err!=nil { return bridgetypes.MessageResponse{},err }
	var messages []struct { Info struct { ID string `json:"id"` } `json:"info"` }
	if json.Unmarshal(previous,&messages)!=nil { return bridgetypes.MessageResponse{},errors.New("invalid V1 conversation") }
	ids:=map[string]bool{}
	for _,m:=range messages { if m.Info.ID!="" { ids[m.Info.ID]=true } }
	if err:=c.SendMessageAsync(ctx,req);err!=nil {
		if ctx.Err()!=nil { return bridgetypes.MessageResponse{},c.abortV1(req.SessionID,ctx.Err()) }
		return bridgetypes.MessageResponse{},err
	}
	result,err:=c.waitV1(ctx,req.SessionID,ids)
	return bridgetypes.MessageResponse{SessionID:req.SessionID,Message:result.Message,Raw:result.Raw},err
}

func (c *Client) abortV1(id string, cause error) error {
	ctx,cancel:=context.WithTimeout(context.Background(),3*time.Second)
	defer cancel()
	var abortErr error
	if c.options.APIVersion!="" { abortErr=c.guardV1(ctx,"/session/"+url.PathEscape(id)) }
	if abortErr==nil { _,abortErr=c.request(ctx,http.MethodPost,c.v1Path("/session/"+url.PathEscape(id)+"/abort"),nil) }
	return fmt.Errorf("session %s: %w (abort error: %v)",id,cause,abortErr)
}

func v1Answer(raw json.RawMessage, previous map[string]bool, strict bool) (string,error) {
	var messages []struct {
		Info struct {
			ID string `json:"id"`
			Role string `json:"role"`
			Error json.RawMessage `json:"error"`
			Time struct { Completed *float64 `json:"completed"` } `json:"time"`
		} `json:"info"`
		Parts []struct {Type string `json:"type"`; Text string `json:"text"`} `json:"parts"`
	}
	if json.Unmarshal(raw,&messages)!=nil { return "",errors.New("invalid V1 conversation") }
	for i:=len(messages)-1;i>=0;i-- {
		m:=messages[i]
		if m.Info.Role!="assistant" || previous[m.Info.ID] { continue }
		if strict && m.Info.ID=="" { return "",errors.New("assistant message missing ID") }
		if len(m.Info.Error)>0 && string(m.Info.Error)!="null" { return "",fmt.Errorf("assistant failed: %s",m.Info.Error) }
		if strict && m.Info.Time.Completed==nil { return "",nil }
		var text []string
		for _,p:=range m.Parts { if p.Type=="text" && strings.TrimSpace(p.Text)!="" { text=append(text,p.Text) } }
		if len(text)==0 { return "",errors.New("assistant completed without a text answer") }
		return strings.Join(text,"\n"),nil
	}
	return "",nil
}
