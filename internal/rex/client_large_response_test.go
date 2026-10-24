package rex

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestLargeFragmentedControlResponseLeavesNextReplyIntact(t *testing.T) {
	front, back := net.Pipe()
	client := NewClient(front, nil)
	defer client.Close()
	defer back.Close()
	title := strings.Repeat("中文🙂-fragment-", 100000)
	done := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(back)
		var req Request
		if err := decoder.Decode(&req); err != nil {
			done <- err
			return
		}
		data, _ := json.Marshal([]SessionInfo{{ID: "owned", Title: title}})
		wire, _ := json.Marshal(Response{ID: req.ID, Data: data, ETag: "large"})
		wire = append(wire, '\n')
		for len(wire) > 0 {
			n := min(4001, len(wire)) // split JSON and multibyte text at arbitrary boundaries
			if _, err := back.Write(wire[:n]); err != nil {
				done <- err
				return
			}
			wire = wire[n:]
		}
		if err := decoder.Decode(&req); err != nil {
			done <- err
			return
		}
		data, _ = json.Marshal(Hello{Version: ProtocolVersion})
		done <- json.NewEncoder(back).Encode(Response{ID: req.ID, Data: data})
	}()
	infos, err := client.List()
	if err != nil || len(infos) != 1 || infos[0].Title != title {
		t.Fatalf("large control reply was truncated or corrupted: %d sessions, %v", len(infos), err)
	}
	hello, err := client.Hello()
	if err != nil || hello.Version != ProtocolVersion {
		t.Fatalf("reply after large response was corrupted: %+v, %v", hello, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
