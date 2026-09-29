package ovn

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

//go:embed testdata/ovn-nb.ovsschema
var nbSchemaJSON []byte

// parseNBSchema parses the embedded OVN Northbound schema, used to type
// reference columns when expanding named UUIDs before a transaction.
func parseNBSchema() (*ovsdb.DatabaseSchema, error) {
	s := &ovsdb.DatabaseSchema{}
	if err := json.Unmarshal(nbSchemaJSON, s); err != nil {
		return nil, fmt.Errorf("parse OVN NB schema: %w", err)
	}
	return s, nil
}

// nbEndpoint is the live OVN Northbound OVSDB endpoint on dom0: the
// standard OVN NB unix socket by default (no native TCP protocol needed),
// overridable via QVM_OVN_ENDPOINT (e.g. "tcp:127.0.0.1:6640").
func nbEndpoint() string {
	if v := os.Getenv("QVM_OVN_ENDPOINT"); v != "" {
		return v
	}
	return "unix:/var/run/ovn/ovnnb_db.sock"
}

// NewLive dials the live OVN Northbound database and returns a monitoring
// Reconciler over it.
func NewLive(ctx context.Context) (*Reconciler, error) {
	var schema ovsdb.DatabaseSchema
	if err := json.Unmarshal(nbSchemaJSON, &schema); err != nil {
		return nil, fmt.Errorf("ovn schema: %w", err)
	}
	clientModel, err := model.NewClientDBModel(schema.Name, Tables())
	if err != nil {
		return nil, err
	}
	c, err := client.NewOVSDBClient(clientModel, client.WithEndpoint(nbEndpoint()))
	if err != nil {
		return nil, err
	}
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	if _, err := c.MonitorAll(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return New(c), nil
}
