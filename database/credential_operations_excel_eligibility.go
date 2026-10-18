package database

import (
	"fmt"
	"strings"
)

// Excel acquisition is limited to paid credential owners; Free remains eligible
// for ordinary Codex credential operations.
func CredentialOperationsExcelEligible(row *AccountRow) bool {
	if !CredentialOperationsEligible(row) {
		return false
	}
	for _, key := range []string{"parent_account_id", "shadow_parent_account_id", "credential_parent_account_id"} {
		value, exists := row.Credentials[key]
		if !exists || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "0" && text != "false" {
			return false
		}
	}
	for _, key := range []string{"is_shadow", "credential_shadow", "shadow"} {
		value := strings.ToLower(strings.TrimSpace(fmt.Sprint(row.Credentials[key])))
		if value == "true" || value == "1" {
			return false
		}
	}
	plan := row.GetCredential("plan_type")
	if strings.TrimSpace(plan) == "" {
		plan = row.GetCredential("chatgpt_plan_type")
	}
	plan = strings.TrimPrefix(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(plan)), "-", "_"), "chatgpt_")
	family := strings.SplitN(plan, "_", 2)[0]
	switch family {
	case "plus", "pro", "team", "teams", "business", "enterprise", "edu", "education", "go":
		return true
	}
	return false
}

// Apply the paid-owner filter before LIMIT so ineligible rows cannot starve
// later eligible accounts in the acquisition queue.
func (db *DB) credentialOpsExcelEligibilitySQL(alias string) string {
	field := func(key string) string {
		if db.isSQLite() {
			return "json_extract(" + alias + ".credentials,'$." + key + "')"
		}
		return alias + ".credentials->>'" + key + "'"
	}
	raw := "COALESCE(NULLIF(" + field("plan_type") + ",'')," + field("chatgpt_plan_type") + ",'')"
	plan := "LOWER(REPLACE(TRIM(" + raw + "),'-','_'))"
	normalized := "(CASE WHEN SUBSTR(" + plan + ",1,8)='chatgpt_' THEN SUBSTR(" + plan + ",9) ELSE " + plan + " END)"
	family := "split_part(" + normalized + ",'_',1)"
	if db.isSQLite() {
		family = "(CASE WHEN INSTR(" + normalized + ",'_')>0 THEN SUBSTR(" + normalized + ",1,INSTR(" + normalized + ",'_')-1) ELSE " + normalized + " END)"
	}
	parts := []string{family + " IN ('plus','pro','team','teams','business','enterprise','edu','education','go')"}
	for _, key := range []string{"parent_account_id", "shadow_parent_account_id", "credential_parent_account_id"} {
		parts = append(parts, "COALESCE(CAST("+field(key)+" AS TEXT),'') IN ('','0','false','null')")
	}
	for _, key := range []string{"is_shadow", "credential_shadow", "shadow"} {
		parts = append(parts, "LOWER(COALESCE(CAST("+field(key)+" AS TEXT),'')) NOT IN ('1','true')")
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}
