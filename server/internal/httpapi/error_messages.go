package httpapi

import (
	"net/http"
	"strings"
	"unicode"
)

// Public API error codes are the stable machine contract. Chinese clients use
// the domain explanation; every other locale receives the concise English
// catalog entry. Responses never concatenate two languages.
var publicErrorMessagesEnglish = map[string]string{
	"account_unavailable":            "This account is currently unavailable",
	"agent_required":                 "An agent must be selected",
	"agent_in_use":                   "Stop active runs, clear pending mentions and pause schedules before deleting this Agent",
	"approval_action_changed":        "The approval action has changed",
	"approval_expired":               "The approval has expired",
	"approval_not_dispatchable":      "The approval cannot be dispatched",
	"attachment_invalid":             "The image attachment is invalid or unsupported",
	"attachment_too_large":           "Each image attachment must be 512 KiB or smaller",
	"attachments_too_large":          "Image attachments must total 768 KiB or less",
	"attachments_too_many":           "No more than four image attachments are allowed",
	"capability_unsupported":         "This capability is not supported",
	"computer_in_use":                "This computer is still in use",
	"conversation_archived":          "The conversation is archived",
	"conversation_busy":              "The conversation has an active run",
	"conversation_too_large":         "This conversation graph is too large to load at once; create a new graph or delete unneeded branches",
	"csrf_failed":                    "CSRF validation failed",
	"csrf_origin":                    "The request origin is not allowed",
	"delete_conflict":                "The resource could not be deleted because its state changed",
	"dependency_unavailable":         "A required component is unavailable. Try again, then check Activity if it keeps failing",
	"goal_already_exists":            "This conversation already has an unfinished goal",
	"goal_not_active":                "The goal is not currently active",
	"goal_not_resumable":             "The goal cannot be resumed from its current state",
	"goal_run_stopping":              "The current goal run is still stopping; try again shortly",
	"goal_terminal":                  "The goal has already ended",
	"goal_turn_limit":                "The goal reached its automatic continuation limit",
	"goal_unavailable":               "Goal mode is unavailable in this runtime",
	"git_discovery_incomplete":       "Git repository discovery was incomplete; refresh the project resources before retrying",
	"git_resource_invalid":           "A Git resource exceeds the Node protocol field limits",
	"git_resources_too_large":        "The project has too many Git resources for one run",
	"internal":                       "This action could not be completed. Try again, then check Activity if it keeps failing",
	"invalid_argument":               "The request is invalid",
	"computer_revoked":               "This computer has been revoked",
	"invalid_session":                "The session is invalid or has expired",
	"maintenance":                    "The service is under maintenance; try again later",
	"local_access_required":          "Change remote access settings from the local window or localhost",
	"remote_access_port_unavailable": "The shared service port could not be opened on the LAN; check the network and release any conflicting listener, then retry",
	"memory_key_conflict":            "A memory entry with this key already exists",
	"node_offline":                   "No eligible node is currently online",
	"not_found":                      "The requested resource was not found",
	"not_own_comment":                "Only the comment's author can edit it",
	"permission_denied":              "Permission denied",
	"project_active":                 "The project still has an active run",
	"rate_limited":                   "Too many requests; try again later",
	"response_too_large":             "The response exceeds the safe transfer limit; request a smaller page",
	"request_too_large":              "The request exceeds the safe transfer limit",
	"run_event_too_large":            "A Run event exceeds the safe payload limit",
	"resource_open_failed":           "The code directory could not be opened",
	"repository_not_found":           "A project Git repository was not found or is no longer available",
	"run_start_payload_too_large":    "The run start payload exceeds the Node protocol frame limit",
	"schedule_concurrency_forbidden": "This scheduled task already has an active run",
	"schedule_no_next_run":           "The cron expression has no future occurrence in this timezone",
	"scheduled_task_paused":          "The scheduled task is paused",
	"skill_in_use":                   "This skill is in use by an Agent or an active run. Unbind it from all Agents in their configuration and stop related runs, then retry uninstalling.",
	"skill_uninstall_failed":         "Skill file removal could not be confirmed; the installation record is retained. Retry uninstalling or reinstall before using it again",
	"skill_uninstall_incomplete":     "Skill files were removed but the installation record could not be committed. Refresh and retry uninstalling, or reinstall before using it again",
	"target_busy":                    "The target conversation has an active run",
	"trigger_conflict":               "The scheduled task trigger conflicts with an existing trigger",
	"unauthenticated":                "Authentication is required",
	"update_conflict":                "The resource could not be updated because its state changed",
	"version_conflict":               "The resource version has changed",
}

func publicErrorMessage(code, message string, status int, acceptLanguage string) string {
	original := strings.TrimSpace(message)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "zh") {
		// Skill usage is a fixed public contract, never arbitrary repository or
		// runtime prose (which may include local paths or private configuration).
		if code == "skill_in_use" {
			return "该技能正在被 Agent 或活动运行使用。请先在 Agent 配置中解除所有绑定，并停止相关运行，再重试卸载。"
		}
		if containsHan(original) {
			return original
		}
	}

	english := publicErrorMessagesEnglish[code]
	if english != "" {
		return english
	}
	if original != "" && !containsHan(original) {
		return original
	}
	return publicErrorMessageForStatus(status)
}

func publicErrorMessageForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "The request is invalid"
	case http.StatusUnauthorized:
		return "Authentication is required or the credentials are invalid"
	case http.StatusForbidden:
		return "The request is forbidden"
	case http.StatusNotFound:
		return "The requested resource was not found"
	case http.StatusConflict:
		return "The request conflicts with the current resource state"
	case http.StatusUnprocessableEntity:
		return "The request prerequisites are not satisfied"
	case http.StatusRequestEntityTooLarge:
		return "The request or response exceeds the safe transfer limit"
	case http.StatusTooManyRequests:
		return "Too many requests; try again later"
	case http.StatusServiceUnavailable:
		return "This action could not be completed. Try again, then check Activity if it keeps failing"
	default:
		return "This action could not be completed. Try again, then check Activity if it keeps failing"
	}
}

func containsHan(value string) bool {
	for _, character := range value {
		if unicode.Is(unicode.Han, character) {
			return true
		}
	}
	return false
}
