// Shared juhe_stats table qualifier for the authorization usage reads.
package authz

// statsTable qualifies a juhe_stats table (PostgreSQL schema-qualified),
// matching the gateway accounts/apikeys stats table helpers.
func (s *Store) statsTable(name string) string {
	return "juhe_stats." + name
}
