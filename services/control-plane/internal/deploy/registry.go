package deploy

// RegisterAll populates the registry. Registration order is the --all deploy
// order: no-dependency services first, then the CA, then certificate consumers.
//
// It lives here rather than in main so every package that maintains a table
// keyed by service name can walk the same list in its tests. Six such tables
// exist, and two of them had already drifted - Harbor was absent from the
// container filters and from reset.sh, so its logs were invisible in the UI and
// its eight containers survived a reset that reported success.
func RegisterAll(engine *Engine) {
	engine.Register(Chrony{})
	engine.Register(Rsyslog{})
	engine.Register(CA{})
	engine.Register(Technitium{})
	// Ingress comes up after DNS so its wildcard host and the bridge-stack
	// routes resolve; it is part of the pre-selected foundation set.
	engine.Register(Traefik{})
	engine.Register(Depot{})
	engine.Register(Keycloak{})
	engine.Register(Authentik{})
	engine.Register(Zitadel{})
	engine.Register(Netbox{})
	engine.Register(S3{})
	engine.Register(SFTP{})
	engine.Register(Mailpit{})
	engine.Register(LLDAP{})
	engine.Register(Samba{})
	engine.Register(KMIP{})
	engine.Register(Harbor{})
	engine.Register(DNSSync{})
}
