package overlay

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/master/httpapi"
)

// maxTestPorts is how many ports an agent tests at once.
const maxTestPorts = 32

var errPeerNotMember = httpapi.Errorf(http.StatusNotFound, "The other node isn't part of the private network.")

// PortTest is the result of connecting from a member to a port that another member publishes
// for it, for a server or a datastore.
type PortTest struct {
	Port        uint32 `json:"port"`
	ServerID    string `json:"serverId,omitempty"`
	DatastoreID string `json:"datastoreId,omitempty"`
	// Error tells why no connection came about; empty if one did.
	Error string `json:"error,omitempty"`
	// Millis is how long connecting took, or failing.
	Millis float64 `json:"millis"`
}

// Test connects from a member to the ports that another member publishes for it, e.g. to find
// out why a proxy doesn't reach a server there. The member connects only to the ports that
// its configuration says the other publishes for it, so it is configured first with those
// that the others report now.
func (s *Service) Test(ctx context.Context, nodeID, peerID string) ([]PortTest, error) {
	peer, published, err := s.prepareTest(ctx, nodeID, peerID)
	if err != nil {
		return nil, err
	}
	tests := make([]PortTest, 0, len(published))
	for chunk := range slices.Chunk(published, maxTestPorts) {
		req := &noryxv1.TestOverlayPeerRequest{PublicKey: peer.PublicKey}
		for _, p := range chunk {
			req.Ports = append(req.Ports, p.GetPort())
		}
		var res *noryxv1.TestOverlayPeerResponse
		err := s.call(ctx, nodeID, func(ctx context.Context, c noryxv1.OverlayServiceClient) (err error) {
			res, err = c.TestOverlayPeer(ctx, req)
			return err
		})
		if err != nil {
			return nil, err
		}
		for i, p := range chunk {
			test := PortTest{Port: p.GetPort(), ServerID: p.GetServerId(), DatastoreID: p.GetDatastoreId(), Error: "The node sent no result."}
			if r := res.GetResults(); i < len(r) {
				test.Error, test.Millis = r[i].GetError(), float64(r[i].GetDurationMicros())/1000
			}
			tests = append(tests, test)
		}
	}
	return tests, nil
}

// prepareTest configures a member with the ports that the others report to publish for it,
// and returns the peer it tests with the ports that this one publishes for it.
func (s *Service) prepareTest(ctx context.Context, nodeID, peerID string) (Member, []*noryxv1.OverlayPublished, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.Settings(ctx)
	members, membersErr := s.Members(ctx)
	if err = cmp.Or(err, membersErr); err != nil {
		return Member{}, nil, err
	}
	i := slices.IndexFunc(members, func(m Member) bool { return m.NodeID == nodeID })
	j := slices.IndexFunc(members, func(m Member) bool { return m.NodeID == peerID })
	switch {
	case i < 0:
		return Member{}, nil, errNotMember
	case j < 0 || i == j:
		return Member{}, nil, errPeerNotMember
	}
	reports, errs := s.reports(ctx, members)
	switch {
	case errs[j] != nil:
		return Member{}, nil, errs[j]
	case reports[peerID].GetAddress() == "":
		return Member{}, nil, httpapi.Errorf(http.StatusConflict, "The other node isn't configured yet. The master configures it again within 5 minutes.")
	}
	if err := s.configure(ctx, settings, members, members[i], reports); err != nil {
		return Member{}, nil, err
	}
	return members[j], publishedFor(reports[peerID], members[i]), nil
}
