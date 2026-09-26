# qlvm

Qubes-like VM isolation on dom0: a native Go toolset that manages isolated,
subnet-scoped VM domains with per-domain gateways, a single OVN-backed SNAT
router to the physical LAN, and a declarative dom0 egress firewall —
configured through a single TOML file.
