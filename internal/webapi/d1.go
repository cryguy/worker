package webapi

import (
	"encoding/json"
	"fmt"

	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
)

// SetupD1 registers global Go functions for D1 database operations.
// D1 stores must be provided via Env.D1 (map of binding name -> D1Store).
func SetupD1(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// __d1_exec(reqIDStr, databaseID, sqlStr, bindingsJSON) -> JSON result or error JSON
	if err := rt.RegisterFunc("__d1_exec", func(reqIDStr, databaseID, sqlStr, bindingsJSON string) (string, error) {
		reqID := core.ParseReqID(reqIDStr)
		state := core.GetRequestState(reqID)
		if state == nil || state.Env == nil || state.Env.D1 == nil {
			return "", fmt.Errorf("D1 not available")
		}

		// Find the D1Store: try direct binding name lookup first,
		// then fall back to D1Bindings reverse lookup by database ID.
		var store core.D1Store
		if s, ok := state.Env.D1[databaseID]; ok {
			store = s
		} else if state.Env.D1Bindings != nil {
			for bindingName, dbID := range state.Env.D1Bindings {
				if dbID == databaseID {
					if s, ok := state.Env.D1[bindingName]; ok {
						store = s
						break
					}
				}
			}
		}
		if store == nil {
			// Last resort: pick the first available store.
			for _, s := range state.Env.D1 {
				store = s
				break
			}
		}
		if store == nil {
			return "", fmt.Errorf("D1 database %q not found", databaseID)
		}

		var bindings []interface{}
		if bindingsJSON != "" && bindingsJSON != "[]" {
			if err := json.Unmarshal([]byte(bindingsJSON), &bindings); err != nil {
				errResult := map[string]string{"error": "invalid bindings JSON: " + err.Error()}
				data, _ := json.Marshal(errResult)
				return string(data), nil
			}
		}

		result, err := store.Exec(sqlStr, bindings)
		if err != nil {
			errResult := map[string]string{"error": err.Error()}
			data, _ := json.Marshal(errResult)
			return string(data), nil
		}

		data, _ := json.Marshal(result)
		return string(data), nil
	}); err != nil {
		return fmt.Errorf("registering __d1_exec: %w", err)
	}

	// Define the __makeD1 factory function.
	d1FactoryJS := `
globalThis.__makeD1 = function(databaseID) {
	function rowsToObjects(result) {
		var results = [];
		if (result.columns && result.rows) {
			for (var i = 0; i < result.rows.length; i++) {
				var obj = {};
				for (var j = 0; j < result.columns.length; j++) {
					obj[result.columns[j]] = result.rows[i][j];
				}
				results.push(obj);
			}
		}
		return results;
	}
	function execSQL(sql, boundValues) {
		var reqID = String(globalThis.__requestID);
		var bindingsJSON = JSON.stringify(boundValues || []);
		var resultStr = __d1_exec(reqID, databaseID, sql, bindingsJSON);
		var result = JSON.parse(resultStr);
		if (result.error) throw new Error(result.error);
		return result;
	}
	function makeStmt(sql) {
		var stmt = {
			_sql: sql,
			_bindings: [],
			bind: function() {
				var newStmt = Object.create(stmt);
				newStmt._bindings = Array.prototype.slice.call(arguments);
				return newStmt;
			},
			first: function(colName) {
				try {
					var result = execSQL(this._sql, this._bindings);
					var rows = rowsToObjects(result);
					if (rows.length === 0) return Promise.resolve(null);
					if (colName !== undefined && colName !== null) {
						return Promise.resolve(rows[0][colName] !== undefined ? rows[0][colName] : null);
					}
					return Promise.resolve(rows[0]);
				} catch(e) { return Promise.reject(e); }
			},
			all: function() {
				try {
					var result = execSQL(this._sql, this._bindings);
					return Promise.resolve({
						results: rowsToObjects(result),
						success: true,
						meta: result.meta || {}
					});
				} catch(e) { return Promise.reject(e); }
			},
			run: function() {
				try {
					var result = execSQL(this._sql, this._bindings);
					return Promise.resolve({ results: [], success: true, meta: result.meta || {} });
				} catch(e) { return Promise.reject(e); }
			},
			raw: function(opts) {
				try {
					var result = execSQL(this._sql, this._bindings);
					var rows = result.rows || [];
					if (opts && opts.columnNames) {
						rows = [result.columns].concat(rows);
					}
					return Promise.resolve(rows);
				} catch(e) { return Promise.reject(e); }
			}
		};
		return stmt;
	}
	return {
		prepare: function(sql) { return makeStmt(sql); },
		batch: function(statements) {
			return new Promise(function(resolve, reject) {
				try {
					var results = [];
					for (var i = 0; i < statements.length; i++) {
						var s = statements[i];
						var result = execSQL(s._sql, s._bindings);
						results.push({
							results: rowsToObjects(result),
							success: true,
							meta: result.meta || {}
						});
					}
					resolve(results);
				} catch(e) { reject(e); }
			});
		},
		exec: function(sql) {
			var reqID = String(globalThis.__requestID);
			return new Promise(function(resolve, reject) {
				try {
					var statements = [];
					var current = '';
					var inStr = false;
					for (var i = 0; i < sql.length; i++) {
						var ch = sql[i];
						if (ch === "'" && !inStr) { inStr = true; current += ch; }
						else if (ch === "'" && inStr) {
							if (i + 1 < sql.length && sql[i + 1] === "'") { current += "''"; i++; }
							else { inStr = false; current += ch; }
						}
						else if (ch === ';' && !inStr) {
							if (current.trim().length > 0) statements.push(current.trim());
							current = '';
						}
						else { current += ch; }
					}
					if (current.trim().length > 0) statements.push(current.trim());
					var count = 0;
					for (var i = 0; i < statements.length; i++) {
						__d1_exec(reqID, databaseID, statements[i], "[]");
						count++;
					}
					resolve({ count: count, duration: 0 });
				} catch(e) { reject(e); }
			});
		},
		dump: function() {
			return Promise.reject(new Error("D1 dump() is not supported in this runtime"));
		}
	};
};
`
	if err := rt.Eval(d1FactoryJS); err != nil {
		return fmt.Errorf("evaluating D1 factory JS: %w", err)
	}

	return nil
}
