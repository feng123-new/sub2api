package service

import (
 "context"
 "net/http"
 "testing"
 "time"

 "github.com/Wei-Shaw/sub2api/internal/config"
 "github.com/stretchr/testify/require"
)

func TestCodexTicketExplicitOptInBoundary(t *testing.T) {
 for _, tc := range []struct{name string; extra map[string]any; enabled bool}{
  {"missing", nil, false},
  {"disabled", map[string]any{codexTicketAccountEnabledKey:false}, false},
  {"invalid_string", map[string]any{codexTicketAccountEnabledKey:"true"}, false},
  {"enabled", map[string]any{codexTicketAccountEnabledKey:true}, true},
  {"model_disabled", map[string]any{codexTicketAccountEnabledKey:true, codexTicketModelsEnabledKey:map[string]any{"gpt-6-astra":false}}, false},
 } {
  t.Run(tc.name, func(t *testing.T){
   acc:=ticketTestAccount(91)
   acc.Status=StatusActive
   acc.Extra=tc.extra
   cfg:=config.OpenAICodexTicketConfig{Enabled:true, FailClosed:true, Models:[]string{"gpt-6-astra"}}
   svc:=ticketTestService(t,cfg,nil)
   require.Equal(t,tc.enabled,CodexTicketHarvestEnabled(acc,"gpt-6-astra"))
   require.Equal(t,tc.enabled,svc.openAICodexTicketBlocksAccount(acc,"gpt-6-astra"))
   statuses:=OpenAICodexTicketStatuses(acc,cfg,time.Now())
   require.Len(t,statuses,1)
   require.Equal(t,tc.enabled,statuses[0].HarvestEnabled)
   require.Equal(t,tc.enabled,statuses[0].Blocked)
   state:=fakeCodexTicketState(292)
   svc.openaiCodexTickets.Store(openAICodexTicketKey(acc.ID,"gpt-6-astra"), &openAICodexTicket{AccountID:acc.ID,Model:"gpt-6-astra",State:state,Length:292,ExpiresAt:time.Now().Add(time.Hour)})
   headers:=http.Header{}
   headers.Set(openAICodexTurnStateHeader,"client-original")
   require.NoError(t,svc.applyOpenAICodexTicket(context.Background(),acc,"gpt-6-astra",headers))
   if tc.enabled {require.Equal(t,state,headers.Get(openAICodexTurnStateHeader))} else {
    require.Equal(t,"client-original",headers.Get(openAICodexTurnStateHeader))
    // Disabled probes must return before attempting any HTTP upstream access.
    svc.probeOnceOpenAICodexTicket(context.Background(),acc,"gpt-6-astra")
   }
  })
 }
}
