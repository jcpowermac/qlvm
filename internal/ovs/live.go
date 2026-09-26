package ovs

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

//go:embed testdata/openvswitch.ovsschema
var ovsSchemaJSON []byte

// ovsEndpoint is the live Open_vSwitch OVSDB endpoint on dom0.
const ovsEndpoint = "tcp:127.0.0.1:6641"

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
	c, err := client.NewOVSDBClient(clientModel, client.WithEndpoint(ovsEndpoint))
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
