// Package ovn models the OVN_Northbound tables qlvm manages and provides a
// declarative reconciler that converges the database onto the desired state.
package ovn

import (
	"github.com/ovn-kubernetes/libovsdb/model"
)

// The structs below model the OVN_Northbound tables qlvm writes. Column
// tags and types follow the real OVN schema (testdata/ovn-nb.ovsschema):
// unbounded sets map to slices, 0..1 sets map to pointers, maps to maps.

// LogicalSwitch is a row in the Logical_Switch table.
type LogicalSwitch struct {
	UUID  string   `ovsdb:"_uuid"`
	Name  string   `ovsdb:"name"`
	Ports []string `ovsdb:"ports"`
	Acls  []string `ovsdb:"acls"`
}

// LogicalRouter is a row in the Logical_Router table.
type LogicalRouter struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	Ports        []string          `ovsdb:"ports"`
	StaticRoutes []string          `ovsdb:"static_routes"`
	NAT          []string          `ovsdb:"nat"`
	Options      map[string]string `ovsdb:"options"`
}

// LogicalRouterPort is a row in the Logical_Router_Port table.
type LogicalRouterPort struct {
	UUID           string            `ovsdb:"_uuid"`
	Name           string            `ovsdb:"name"`
	MAC            string            `ovsdb:"mac"`
	Networks       []string          `ovsdb:"networks"`
	Options        map[string]string `ovsdb:"options"`
	HaChassisGroup *string           `ovsdb:"ha_chassis_group"`
}

// LogicalSwitchPort is a row in the Logical_Switch_Port table.
type LogicalSwitchPort struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	Type         string            `ovsdb:"type"`
	Options      map[string]string `ovsdb:"options"`
	Addresses    []string          `ovsdb:"addresses"`
	PortSecurity []string          `ovsdb:"port_security"`
}

// ACL is a row in the ACL table.
type ACL struct {
	UUID            string  `ovsdb:"_uuid"`
	Name            *string `ovsdb:"name"`
	Priority        int     `ovsdb:"priority"`
	Direction       string  `ovsdb:"direction"`
	MatchExpression string  `ovsdb:"match"`
	Action          string  `ovsdb:"action"`
}

// LogicalRouterNAT is a row in the NAT table.
type LogicalRouterNAT struct {
	UUID        string            `ovsdb:"_uuid"`
	ExternalIP  string            `ovsdb:"external_ip"`
	ExternalMAC *string           `ovsdb:"external_mac"`
	LogicalIP   string            `ovsdb:"logical_ip"`
	Type        string            `ovsdb:"type"`
	Priority    int               `ovsdb:"priority"`
	Options     map[string]string `ovsdb:"options"`
}

// LogicalRouterStaticRoute is a row in the Logical_Router_Static_Route table.
type LogicalRouterStaticRoute struct {
	UUID       string  `ovsdb:"_uuid"`
	IPPrefix   string  `ovsdb:"ip_prefix"`
	Nexthop    string  `ovsdb:"nexthop"`
	OutputPort *string `ovsdb:"output_port"`
	RouteTable string  `ovsdb:"route_table"`
}

// HAChassisGroup is a row in the HA_Chassis_Group table.
type HAChassisGroup struct {
	UUID      string   `ovsdb:"_uuid"`
	Name      string   `ovsdb:"name"`
	HAChassis []string `ovsdb:"ha_chassis"`
}

// HAChassis is a row in the HA_Chassis table; ChassisName must match a
// Chassis.name known to ovn-controller.
type HAChassis struct {
	UUID        string `ovsdb:"_uuid"`
	ChassisName string `ovsdb:"chassis_name"`
	Priority    int    `ovsdb:"priority"`
}

// SBChassis is a row in the OVN_Southbound Chassis table: the live chassis
// registry ovn-controller maintains. Name is the chassis identifier
// (a UUID on stock Fedora installs).
type SBChassis struct {
	UUID     string `ovsdb:"_uuid"`
	Name     string `ovsdb:"name"`
	Hostname string `ovsdb:"hostname"`
}

// SBTables is the OVN_Southbound table set qlvm reads; used to build the
// read-side SB client model (Chassis is the only table install needs).
func SBTables() map[string]model.Model {
	return map[string]model.Model{
		"Chassis": &SBChassis{},
	}
}

// Tables maps the OVN_Northbound table names qlvm manages to their models.
// It is the table set used to build the libovsdb client DB model.
func Tables() map[string]model.Model {
	return map[string]model.Model{
		"Logical_Switch":              &LogicalSwitch{},
		"Logical_Router":              &LogicalRouter{},
		"Logical_Router_Port":         &LogicalRouterPort{},
		"Logical_Switch_Port":         &LogicalSwitchPort{},
		"ACL":                         &ACL{},
		"NAT":                         &LogicalRouterNAT{},
		"Logical_Router_Static_Route": &LogicalRouterStaticRoute{},
		"HA_Chassis_Group":            &HAChassisGroup{},
		"HA_Chassis":                  &HAChassis{},
	}
}
