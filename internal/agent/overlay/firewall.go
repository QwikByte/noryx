package overlay

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// table is the nftables table of the network. It works next to Docker's rules, whether
// Docker writes them with iptables or nftables, as its chains only drop.
var table = &nftables.Table{Family: nftables.TableFamilyINet, Name: "noryx"}

// baseChain is a chain of the table that accepts what its rules don't drop.
type baseChain struct {
	name     string
	hook     *nftables.ChainHook
	priority nftables.ChainPriority
	rules    []rule
}

// rule is a rule of the table with its comment, which nft list shows and by which
// checkFirewall recognizes it.
type rule struct {
	comment string
	exprs   []expr.Any
}

// chains returns the chains of the table for st:
//   - Packets for the node's address in the network only come in through the interface, not
//     from neighbours on other interfaces, which Docker would forward to published ports.
//   - Nothing in the network reaches the services of the node itself, such as SSH or the agent.
//   - From the network, a published port is only reached by its client, and nothing else is
//     forwarded, e.g. directly to a container.
//   - TCP connections through the interface fit its MTU.
func chains(st State) []baseChain {
	addr := st.Address.Addr()
	forward := []rule{
		{"TCP from " + Interface + " fits its MTU", clampMSS(expr.MetaKeyIIFNAME)},
		{"TCP to " + Interface + " fits its MTU", clampMSS(expr.MetaKeyOIFNAME)},
	}
	for _, id := range slices.Sorted(maps.Keys(st.Clients)) {
		c := st.Clients[id]
		for _, client := range c.addresses() {
			forward = append(forward, rule{
				fmt.Sprintf("%s reaches %s", client, netip.AddrPortFrom(addr, c.Port)),
				join(iifname(expr.CmpOpEq, Interface), ipv4, tcp, ctOriginal(expr.CtKeyDST, addr.AsSlice()),
					ctOriginal(expr.CtKeyPROTODST, binaryutil.BigEndian.PutUint16(c.Port)), saddr(client), accept),
			})
		}
	}
	forward = append(forward, rule{"nothing else from " + Interface, join(iifname(expr.CmpOpEq, Interface), ctNew, drop)})
	return []baseChain{
		{"prerouting", nftables.ChainHookPrerouting, -150, []rule{{
			fmt.Sprintf("%s only through %s", addr, Interface),
			join(ipv4, daddr(addr), iifname(expr.CmpOpNeq, Interface), iifname(expr.CmpOpNeq, "lo"), drop),
		}}},
		{"input", nftables.ChainHookInput, -1, []rule{{"nothing from " + Interface, join(iifname(expr.CmpOpEq, Interface), ctNew, drop)}}},
		{"forward", nftables.ChainHookForward, -1, forward},
	}
}

// writeFirewall replaces the table of the network in one transaction.
func writeFirewall(st State) error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	conn.AddTable(table) // so that deleting it never fails
	conn.DelTable(table)
	conn.AddTable(table)
	for _, c := range chains(st) {
		added := conn.AddChain(&nftables.Chain{
			Name: c.name, Table: table, Type: nftables.ChainTypeFilter, Hooknum: c.hook,
			Priority: nftables.ChainPriorityRef(c.priority), Policy: new(nftables.ChainPolicyAccept),
		})
		for _, r := range c.rules {
			// User data of the type 0 (NFTNL_UDATA_RULE_COMMENT) is the comment that nft shows.
			comment := append([]byte{0, byte(len(r.comment) + 1)}, r.comment+"\x00"...) //nolint:gosec // far shorter than 255 bytes
			conn.AddRule(&nftables.Rule{Table: table, Chain: added, Exprs: r.exprs, UserData: comment})
		}
	}
	return conn.Flush()
}

// ErrNoTable tells that the table of the network is missing, e.g. as something flushed the
// node's rules.
var ErrNoTable = errors.New("the nftables table inet noryx is missing")

// checkFirewall returns nil if the table of the network has the chains and rules that
// writeFirewall writes for st, ErrNoTable if there is none, or else what differs. It knows
// the rules by their comments, which only someone on the node could change; chains that
// someone adds to the table there can only drop more.
func checkFirewall(st State) error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	tables, err := conn.ListTablesOfFamily(table.Family)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(tables, func(t *nftables.Table) bool { return t.Name == table.Name }) {
		return ErrNoTable
	}
	present, err := conn.ListChainsOfTableFamily(table.Family)
	if err != nil {
		return err
	}
	for _, want := range chains(st) {
		i := slices.IndexFunc(present, func(c *nftables.Chain) bool {
			return c.Table != nil && c.Table.Name == table.Name && c.Name == want.name
		})
		if i < 0 {
			return fmt.Errorf("the chain %s is missing", want.name)
		}
		c := present[i]
		if c.Type != nftables.ChainTypeFilter || c.Hooknum == nil || *c.Hooknum != *want.hook || c.Priority == nil ||
			*c.Priority != want.priority || c.Policy == nil || *c.Policy != nftables.ChainPolicyAccept {
			return fmt.Errorf("the chain %s has another type, hook, priority or policy", want.name)
		}
		comments, err := ruleComments(want.name)
		if err != nil {
			return err
		}
		if len(comments) != len(want.rules) {
			return fmt.Errorf("the chain %s has %d rules instead of %d", want.name, len(comments), len(want.rules))
		}
		for j, r := range want.rules {
			if comments[j] != r.comment {
				return fmt.Errorf("the chain %s lacks the rule %q", want.name, r.comment)
			}
		}
	}
	return nil
}

// ruleComments returns the comments of the rules of a chain of the table, in their order. It
// reads them itself, as the nftables package fails to read the direction of ct expressions,
// which the kernel sends 8 bits wide.
func ruleComments(chain string) ([]string, error) {
	conn, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: unix.NFTA_RULE_TABLE, Data: []byte(table.Name + "\x00")},
		{Type: unix.NFTA_RULE_CHAIN, Data: []byte(chain + "\x00")},
	})
	if err != nil {
		return nil, err
	}
	msgs, err := conn.Execute(netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(unix.NFNL_SUBSYS_NFTABLES<<8 | unix.NFT_MSG_GETRULE), Flags: netlink.Request | netlink.Dump},
		Data:   append([]byte{byte(table.Family), unix.NFNETLINK_V0, 0, 0}, attrs...), // struct nfgenmsg
	})
	if err != nil {
		return nil, err
	}
	comments := make([]string, len(msgs))
	for i, m := range msgs {
		if len(m.Data) < 4 {
			return nil, errors.New("a rule without a header")
		}
		ad, err := netlink.NewAttributeDecoder(m.Data[4:])
		if err != nil {
			return nil, err
		}
		for ad.Next() {
			if ad.Type() == unix.NFTA_RULE_USERDATA {
				comments[i] = comment(ad.Bytes())
			}
		}
		if err := ad.Err(); err != nil {
			return nil, err
		}
	}
	return comments, nil
}

// comment returns the comment in the user data of a rule: type, length and value, such as
// "text\x00", of each entry.
func comment(data []byte) string {
	for len(data) >= 2 && int(data[1]) <= len(data)-2 {
		value := data[2 : 2+int(data[1])]
		if data[0] == 0 {
			return string(bytes.TrimRight(value, "\x00"))
		}
		data = data[2+len(value):]
	}
	return ""
}

// removeFirewall removes the table of the network, if there is one.
func removeFirewall() error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	conn.AddTable(table)
	conn.DelTable(table)
	return conn.Flush()
}

func join(parts ...[]expr.Any) []expr.Any { return slices.Concat(parts...) }

var (
	drop   = []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}
	accept = []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}}
	ipv4   = meta(expr.MetaKeyNFPROTO, unix.NFPROTO_IPV4)
	tcp    = meta(expr.MetaKeyL4PROTO, unix.IPPROTO_TCP)
	ctNew  = []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitNEW), Xor: make([]byte, 4)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)},
	}
)

func meta(key expr.MetaKey, value byte) []expr.Any {
	return []expr.Any{&expr.Meta{Key: key, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{value}}}
}

// iifname compares the name of the interface a packet came in through.
func iifname(op expr.CmpOp, name string) []expr.Any {
	return []expr.Any{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1}, &expr.Cmp{Op: op, Register: 1, Data: ifname(name)}}
}

func ifname(name string) []byte {
	b := make([]byte, unix.IFNAMSIZ)
	copy(b, name)
	return b
}

// daddr and saddr match the destination and source of IPv4 packets.
func daddr(a netip.Addr) []expr.Any { return address(16, a) }
func saddr(a netip.Addr) []expr.Any { return address(12, a) }

func address(offset uint32, a netip.Addr) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: a.AsSlice()},
	}
}

// ctOriginal matches a part of the connection as it was before Docker forwarded it to a
// container, e.g. its destination port.
func ctOriginal(key expr.CtKey, value []byte) []expr.Any {
	return []expr.Any{
		&expr.Ct{Register: 1, Key: key, Direction: 0}, // IP_CT_DIR_ORIGINAL
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: value},
	}
}

// clampMSS fits TCP connections that come in or go out through the interface to its MTU.
func clampMSS(direction expr.MetaKey) []expr.Any {
	return join(
		[]expr.Any{&expr.Meta{Key: direction, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(Interface)}},
		tcp,
		[]expr.Any{
			// tcp flags & (syn | rst) == syn
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 13, Len: 1},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 1, Mask: []byte{0x06}, Xor: []byte{0}},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x02}},
			// tcp option maxseg size set rt mtu
			&expr.Rt{Register: 1, Key: expr.RtTCPMSS},
			&expr.Exthdr{SourceRegister: 1, Type: 2, Offset: 2, Len: 2, Op: expr.ExthdrOpTcpopt},
		},
	)
}
