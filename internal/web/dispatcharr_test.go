package web

import "testing"

func TestSaveDispatcharrMediaServer(t *testing.T) {
	app := newApp(t)
	rec := app.post("/configuration/media-servers", mediaServerForm("Live TV", "dispatcharr", "http://dispatcharr.lan:9191", "dispatcharr-secret"))
	expectRedirect(t, rec, "/configuration?tab=connections")
	servers, err := app.db.ListMediaServers()
	if err != nil || len(servers) != 1 || servers[0].Type != "dispatcharr" || servers[0].CredentialEncrypted == "dispatcharr-secret" {
		t.Fatalf("Dispatcharr servers = %+v, %v", servers, err)
	}
	expectBody(t, app.get("/configuration/media-servers/new"), `value="dispatcharr"`)
}
