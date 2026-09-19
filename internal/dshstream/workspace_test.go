package dshstream

import (
	"encoding/json"
	"testing"
	"time"
)

// decodeRawArray decodes a frame field that carries a JSON array of strings or
// objects, so the test asserts the wire shape rather than a Go struct that
// could agree with the encoder by construction.
func decodeRawArray[T any](t *testing.T, raw json.RawMessage) []T {
	t.Helper()
	var values []T
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("field is not a JSON array: %v", err)
	}
	return values
}

// workspaceStreamConfig wires a registry the test can drive: the baseline is
// fixed and every update pushed into the channel is delivered to the open
// stream, which is exactly the seam the serve command uses.
func workspaceStreamConfig(updates chan WorkspaceUpdate) Config {
	return Config{
		Workspaces: func() WorkspaceBaseline {
			return WorkspaceBaseline{
				Items: []WorkspaceView{{
					WorkspaceID: "ws-one",
					Path:        "/srv/one",
					Title:       "one",
					SessionIDs:  []string{"run-1"},
					CreatedAt:   "2026-01-01T00:00:00Z",
					UpdatedAt:   "2026-01-01T00:00:00Z",
				}},
				ArchivedSessionIDs: []string{"run-archived"},
			}
		},
		WorkspaceUpdates: func(observe func(WorkspaceUpdate)) func() {
			go func() {
				for update := range updates {
					observe(update)
				}
			}()
			return func() {}
		},
	}
}

func TestWorkspaceFollowPublishesBaseline(t *testing.T) {
	updates := make(chan WorkspaceUpdate, 4)
	f := newFixture(t, workspaceStreamConfig(updates))
	conn := f.mustDial(t)
	openStream(t, conn, "workspace", "workspace/follow", "{}")

	baseline := readItem(t, conn, "workspace")
	assertKeys(t, baseline, "type", "value")
	assertField(t, baseline, "type", "baseline")
	value := decodeValueObject(t, baseline["value"])
	assertKeys(t, value, "items", "archivedSessionIds")
	items := decodeRawArray[map[string]json.RawMessage](t, value["items"])
	if len(items) != 1 {
		t.Fatalf("baseline items = %#v, want one row", value["items"])
	}
	row := items[0]
	assertKeys(t, row, "workspaceId", "path", "title", "sessionIds", "createdAt", "updatedAt")
	assertField(t, row, "workspaceId", "ws-one")
	assertField(t, row, "path", "/srv/one")
	archived := decodeRawArray[string](t, value["archivedSessionIds"])
	if len(archived) != 1 || archived[0] != "run-archived" {
		t.Fatalf("baseline archivedSessionIds = %#v, want [run-archived]", value["archivedSessionIds"])
	}
}

func TestWorkspaceFollowDeliversIncrements(t *testing.T) {
	updates := make(chan WorkspaceUpdate, 4)
	f := newFixture(t, workspaceStreamConfig(updates))
	conn := f.mustDial(t)
	openStream(t, conn, "workspace", "workspace/follow", "{}")
	readItem(t, conn, "workspace")

	// Every increment kind the protocol defines is delivered as its own frame,
	// so a client that only handles one of them still sees the others.
	row := WorkspaceView{WorkspaceID: "ws-two", Path: "/srv/two", Title: "two", SessionIDs: []string{}, CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z"}
	updates <- WorkspaceUpdate{Kind: "upsert", Workspace: &row}
	upsert := readItem(t, conn, "workspace")
	assertField(t, upsert, "type", "upsert")
	added := decodeValueObject(t, upsert["workspace"])
	assertField(t, added, "workspaceId", "ws-two")

	updates <- WorkspaceUpdate{Kind: "remove", WorkspaceID: "ws-one"}
	remove := readItem(t, conn, "workspace")
	assertKeys(t, remove, "type", "workspaceId")
	assertField(t, remove, "type", "remove")
	assertField(t, remove, "workspaceId", "ws-one")

	updates <- WorkspaceUpdate{Kind: "order", WorkspaceIDs: []string{"ws-two"}}
	order := readItem(t, conn, "workspace")
	assertField(t, order, "type", "order")
	orderIDs := decodeRawArray[string](t, order["workspaceIds"])
	if len(orderIDs) != 1 || orderIDs[0] != "ws-two" {
		t.Fatalf("order frame workspaceIds = %#v, want [ws-two]", order["workspaceIds"])
	}

	updates <- WorkspaceUpdate{Kind: "archived", ArchivedSessionIDs: []string{"run-2"}}
	archived := readItem(t, conn, "workspace")
	assertField(t, archived, "type", "archived")
	archivedIDs := decodeRawArray[string](t, archived["archivedSessionIds"])
	if len(archivedIDs) != 1 || archivedIDs[0] != "run-2" {
		t.Fatalf("archived frame archivedSessionIds = %#v, want [run-2]", archived["archivedSessionIds"])
	}
}

func TestWorkspaceFollowRequiresEmptyArgs(t *testing.T) {
	updates := make(chan WorkspaceUpdate, 1)
	f := newFixture(t, workspaceStreamConfig(updates))
	conn := f.mustDial(t)
	openStream(t, conn, "workspace", "workspace/follow", `{"unexpected":true}`)
	kind, failure := readStreamEnd(t, conn, "workspace")
	if kind != "error" {
		t.Fatalf("terminal frame = %q, want error", kind)
	}
	assertField(t, failure, "code", codeArgumentsInvalid)
}

// A host with no registry still has to answer the stream: the client treats an
// immediate end as a lost carrier and reconnects, so the baseline is empty and
// the carrier stays open.
func TestWorkspaceFollowWithoutRegistryKeepsCarrierOpen(t *testing.T) {
	f := newFixture(t, Config{})
	conn := f.mustDial(t)
	openStream(t, conn, "workspace", "workspace/follow", "{}")

	baseline := readItem(t, conn, "workspace")
	assertField(t, baseline, "type", "baseline")
	value := decodeValueObject(t, baseline["value"])
	items := decodeRawArray[map[string]json.RawMessage](t, value["items"])
	if len(items) != 0 {
		t.Fatalf("baseline items = %#v, want an empty list", value["items"])
	}
	// No end frame within a moment of the baseline: the stream is still there.
	deadline := time.Now().Add(250 * time.Millisecond)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("the stream sent a frame after an empty baseline")
	}
}
