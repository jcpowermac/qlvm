package ovs

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

//go:embed testdata/openvswitch.ovsschema
var ovsSchemaJSON []byte

// parseOVSSchema parses the embedded Open_vSwitch schema, used to type
// reference columns when expanding named UUIDs before a transaction.
func parseOVSSchema() (*ovsdb.DatabaseSchema, error) {
	s := &ovsdb.DatabaseSchema{}
	if err := json.Unmarshal(ovsSchemaJSON, s); err != nil {
		return nil, fmt.Errorf("parse OVS schema: %w", err)
	}
	return s, nil
}

// ovsEndpoint is the live Open_vSwitch OVSDB endpoint on dom0: the
// standard OVS unix socket by default (no native TCP protocol needed),
// overridable via QVM_OVS_ENDPOINT (e.g. "tcp:127.0.0.1:6641").
func ovsEndpoint() string {
	if v := os.Getenv("QVM_OVS_ENDPOINT"); v != "" {
		return v
	}
	return "unix:/var/run/openvswitch/db.sock"
}

// NewLive dials the live Open_vSwitch database and returns a monitoring
// Reconciler over it.
func NewLive(ctx context.Context) (*Reconciler, error) {
	var schema ovsdb.DatabaseSchema
	if err := json.Unmarshal(ovsSchemaJSON, &schema); err != nil {
		return nil, fmt.Errorf("ovs schema: %w", err)
	}
	clientModel, err := model.NewClientDBModel(schema.Name, Tables())
	if err != nil {
		return nil, err
	}
	c, err := client.NewOVSDBClient(clientModel, client.WithEndpoint(ovsEndpoint()))
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
