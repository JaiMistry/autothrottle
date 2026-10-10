package db

import "testing"

// New entries arrive mapped to their counterparts. Also the only test that
// executes the upsert, which does not parse without its WHERE clause.
func TestSeedingMappingsForNewEntries(t *testing.T) {
	d := newTestDB(t)

	msID, err := d.CreateMediaServer(MediaServer{Name: "Plex", Type: "plex", URL: "http://s", CredentialEncrypted: "x", Enabled: true})
	if err != nil {
		t.Fatalf("creating media server: %v", err)
	}
	dlID, err := d.CreateDownloader(Downloader{Name: "qb", Type: "qbittorrent", URL: "http://d", Enabled: true})
	if err != nil {
		t.Fatalf("creating downloader: %v", err)
	}

	if err := d.EnableMappingsForDownloader(dlID); err != nil {
		t.Fatalf("seeding for downloader: %v", err)
	}
	mappings, err := d.ListMappings()
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(mappings) != 1 || !mappings[0].Enabled {
		t.Fatalf("want one enabled mapping, got %+v", mappings)
	}

	// A second pass must not disturb a choice already made.
	if err := d.SetMappingForPair(msID, dlID, false); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	if err := d.EnableMappingsForMediaServer(msID); err != nil {
		t.Fatalf("re-seeding: %v", err)
	}
	mappings, err = d.ListMappings()
	if err != nil {
		t.Fatalf("listing again: %v", err)
	}
	if len(mappings) != 1 || mappings[0].Enabled {
		t.Fatalf("seeding overwrote an explicit choice: %+v", mappings)
	}
}

// "qBittorrent" must not sort after every capitalised name.
func TestListsSortCaseInsensitively(t *testing.T) {
	d := newTestDB(t)
	for _, name := range []string{"Transmission", "qBittorrent"} {
		addTestDownloader(t, d, name)
	}
	dls, err := d.ListDownloaders()
	if err != nil {
		t.Fatal(err)
	}
	if dls[0].Name != "qBittorrent" {
		t.Errorf("downloaders in order %s, %s", dls[0].Name, dls[1].Name)
	}
}
