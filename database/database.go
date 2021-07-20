package database

import (
	"log"

	"github.com/rqlite/gorqlite"
)

const NETWORKS_TABLE_NAME = "networks"
const NODES_TABLE_NAME = "nodes"
const USERS_TABLE_NAME = "users"
const DNS_TABLE_NAME = "dns"
const EXT_CLIENT_TABLE_NAME = "extclients"
const INT_CLIENTS_TABLE_NAME = "intclients"
const DATABASE_FILENAME = "netmaker.db"

var Database gorqlite.Connection

func InitializeDatabase() error {

	conn, err := gorqlite.Open("http://")
	if err != nil {
		return err
	}

	// sqliteDatabase, _ := sql.Open("sqlite3", "./database/"+dbFilename)
	Database = conn
	Database.SetConsistencyLevel("strong")
	createTables()
	return nil
}

func createTables() {
	createTable(NETWORKS_TABLE_NAME)
	createTable(NODES_TABLE_NAME)
	createTable(USERS_TABLE_NAME)
	createTable(DNS_TABLE_NAME)
	createTable(EXT_CLIENT_TABLE_NAME)
	createTable(INT_CLIENTS_TABLE_NAME)
}

func createTable(tableName string) error {
	_, err := Database.WriteOne("CREATE TABLE IF NOT EXISTS " + tableName + " (key TEXT NOT NULL UNIQUE PRIMARY KEY, value TEXT)")
	if err != nil {
		return err
	}
	return nil
}

func Insert(key string, value string, tableName string) error {
	_, err := Database.WriteOne("INSERT OR REPLACE INTO " + tableName + " (key, value) VALUES ('" + key + "', '" + value + "')")
	if err != nil {
		return err
	}
	return nil
}

func DeleteRecord(tableName string, key string) error {
	_, err := Database.WriteOne("DELETE FROM " + tableName + " WHERE key = \"" + key + "\"")
	if err != nil {
		return err
	}
	return nil
}

func DeleteAllRecords(tableName string) error {
	_, err := Database.WriteOne("DELETE TABLE " + tableName)
	if err != nil {
		return err
	}
	err = createTable(tableName)
	if err != nil {
		return err
	}
	return nil
}

func FetchRecord(tableName string, key string) (string, error) {
	results, err := FetchRecords(tableName)
	if err != nil {
		return "", err
	}
	return results[key], nil
}

func FetchRecords(tableName string) (map[string]string, error) {
	row, err := Database.QueryOne("SELECT * FROM " + tableName + " ORDER BY key")
	if err != nil {
		return nil, err
	}
	records := make(map[string]string)
	for row.Next() { // Iterate and fetch the records from result cursor
		var key string
		var value string
		row.Scan(&key, &value)
		records[key] = value
	}
	log.Println(tableName, records)
	return records, nil
}
