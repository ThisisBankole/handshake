package db

// Telemetry counters are local feature-usage tallies with fixed names
// ("checkpoints", "restores", ...). They are only ever drained into the
// anonymous daily heartbeat; nothing here identifies a project or session.

func (d *Database) IncrementTelemetryCounter(name string) error {
	_, err := d.db.Exec(`INSERT INTO telemetry_counters (name, count) VALUES (?, 1)
		ON CONFLICT(name) DO UPDATE SET count = count + 1`, name)
	return err
}

// DrainTelemetryCounters returns all counters and resets them in the same
// transaction, so a concurrent increment is never counted twice or lost.
func (d *Database) DrainTelemetryCounters() (map[string]int64, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query("SELECT name, count FROM telemetry_counters WHERE count > 0")
	if err != nil {
		return nil, err
	}
	counters := map[string]int64{}
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			rows.Close()
			return nil, err
		}
		counters[name] = count
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec("DELETE FROM telemetry_counters"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counters, nil
}

// ActiveAgentsSince lists the distinct agent names with session activity at or
// after the given Unix time. Names come from the fixed adapter set; no session
// content or paths are involved.
func (d *Database) ActiveAgentsSince(sinceUnix int64) ([]string, error) {
	rows, err := d.db.Query("SELECT DISTINCT agent FROM sessions WHERE updated_at >= ? ORDER BY agent", sinceUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []string{}
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}
