package database

import "context"

// IPv6EgressLeasedIPs includes addresses still reserved by old live streams,
// even after a binding moved and its initial cooldown elapsed.
func (db *DB) IPv6EgressLeasedIPs(ctx context.Context) ([]string, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT DISTINCT ip FROM ipv6_egress_leases ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ips := []string{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}
