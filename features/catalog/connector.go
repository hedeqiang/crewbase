package catalog

// Built-in connectors: catalog entries with "builtin": "<kind>" (and
// "product") are not MCP servers but features compiled into Crewbase
// (e.g. builtin "google" → features/google). Their plugin provides a
// Connector under ConnectorKey(kind); the catalog asks it for the entry's
// state and hands install / uninstall to it. Installing returns the URL the
// person signs in at (AuthURL) when the connector needs one.

import (
	"context"
	"errors"

	"github.com/hedeqiang/crewbase/kernel"
)

// KindBuiltin is Item.Kind of a built-in connector entry.
const KindBuiltin = "builtin"

// ConnectorKey is the service key a connector for builtin kind provides.
func ConnectorKey(kind string) string { return "catalog-connector/" + kind }

// ConnectorState is a builtin entry's state as the connector reports it.
type ConnectorState struct {
	Installed bool   `json:"installed"`
	Scope     string `json:"scope,omitempty"`
	// Configured: the connector can be used in this build (e.g. an OAuth
	// client is set up). When false, Note says why.
	Configured bool   `json:"configured"`
	Account    string `json:"account,omitempty"`    // e.g. the signed-in email
	NeedsLogin bool   `json:"needsLogin,omitempty"` // sign-in expired / revoked
	Note       Text   `json:"note,omitzero"`
}

// Connector is implemented by the plugin behind a builtin kind.
type Connector interface {
	State(product string) ConnectorState
	// Connect starts connecting product for scope ("" / "global", an agent
	// id or team:<id>); it returns a sign-in URL to open ("" = done).
	Connect(product, scope string) (authURL string, err error)
	Disconnect(ctx context.Context, product string) error
}

// ErrNoConnector means the plugin behind a builtin entry is not running.
var ErrNoConnector = errors.New("this connector is not available in this build")

func (s *Service) connector(kind string) Connector {
	v, _ := kernel.Lookup[Connector](s.c, ConnectorKey(kind))
	return v
}

func (s *Service) builtinItem(p Preset) Item {
	it := Item{ID: p.ID, Kind: KindBuiltin, Category: p.Category, Name: p.Name, Icon: p.Icon, Desc: p.Desc,
		Auth: p.Auth, Docs: p.Docs, Setup: p.Setup, Builtin: p.Builtin, Product: p.Product}
	if cn := s.connector(p.Builtin); cn != nil {
		st := cn.State(p.Product)
		it.Installed, it.Scope, it.Connector = st.Installed, st.Scope, &st
	} else {
		it.Connector = &ConnectorState{Note: Text{Zh: "此版本未包含该连接器", En: "This connector is not included in this build"}}
	}
	return it
}

func (s *Service) installBuiltin(p Preset, scope string) (Result, error) {
	cn := s.connector(p.Builtin)
	if cn == nil {
		return Result{}, ErrNoConnector
	}
	u, err := cn.Connect(p.Product, scope)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindBuiltin, AuthURL: u}, nil
}

func (s *Service) uninstallBuiltin(p Preset) error {
	cn := s.connector(p.Builtin)
	if cn == nil {
		return ErrMissing
	}
	if !cn.State(p.Product).Installed {
		return ErrMissing
	}
	return cn.Disconnect(context.Background(), p.Product)
}
