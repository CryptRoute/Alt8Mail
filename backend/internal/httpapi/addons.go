package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type addonFieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type addonField struct {
	Key         string             `json:"key"`
	Label       string             `json:"label"`
	Type        string             `json:"type"`
	Placeholder string             `json:"placeholder,omitempty"`
	Help        string             `json:"help,omitempty"`
	Default     string             `json:"default,omitempty"`
	Required    bool               `json:"required"`
	Secret      bool               `json:"secret"`
	Options     []addonFieldOption `json:"options,omitempty"`
}

type addonAction struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Surface     string `json:"surface"`
}

type addonDefinition struct {
	ID                  string
	Name                string
	Vendor              string
	Category            string
	Description         string
	Tagline             string
	Icon                string
	Color               string
	LogoAsset           string
	WebsiteURL          string
	SupportURL          string
	PrivacyURL          string
	DocsURL             string
	ConnectorVersion    string
	ConnectionScope     string
	AuthType            string
	SelfService         bool
	RequiresTenantSetup bool
	Scopes              []string
	Permissions         []string
	Actions             []addonAction
	Fields              []addonField
	SortOrder           int
}

var addonDefinitions = []addonDefinition{
	{
		ID: "deepl", Name: "DeepL", Vendor: "DeepL SE", Category: "Translation",
		Description: "Translate an email into your preferred language without leaving the reader.",
		Icon:        "i-translate", Color: "#0f2b46", LogoAsset: "deepl.svg",
		Tagline:    "Translate any email without leaving the reader.",
		WebsiteURL: "https://www.deepl.com", SupportURL: "https://support.deepl.com",
		PrivacyURL: "https://www.deepl.com/privacy", DocsURL: "https://developers.deepl.com/docs",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "api_key", SelfService: true,
		Scopes: []string{"translate:text"},
		Permissions: []string{
			"Read the selected email's subject and body",
			"Send that text to DeepL for translation",
		},
		Actions: []addonAction{
			{ID: "translate", Label: "Translate email", Description: "Translate the selected message with DeepL.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiKey", Label: "DeepL API key", Type: "password", Required: true, Secret: true, Placeholder: "DeepL API key", Help: "Free API keys end in :fx. The key is encrypted before it is stored."},
			{Key: "targetLanguage", Label: "Translate into", Type: "select", Default: "EN-US", Required: true, Options: []addonFieldOption{
				{Value: "EN-US", Label: "English (US)"}, {Value: "EN-GB", Label: "English (UK)"},
				{Value: "ES", Label: "Spanish"}, {Value: "FR", Label: "French"}, {Value: "DE", Label: "German"},
				{Value: "IT", Label: "Italian"}, {Value: "PT-BR", Label: "Portuguese (Brazil)"},
				{Value: "NL", Label: "Dutch"}, {Value: "JA", Label: "Japanese"}, {Value: "KO", Label: "Korean"},
				{Value: "ZH", Label: "Chinese"}, {Value: "AR", Label: "Arabic"}, {Value: "RU", Label: "Russian"},
			}},
		},
		SortOrder: 10,
	},
	{
		ID: "todoist", Name: "Todoist", Vendor: "Doist", Category: "Tasks",
		Description: "Turn the email you are reading into a Todoist task with its sender and context attached.",
		Icon:        "i-check-circle", Color: "#e44332", LogoAsset: "todoist.svg",
		Tagline:    "Turn an email into a task with the context attached.",
		WebsiteURL: "https://todoist.com", SupportURL: "https://todoist.com/help",
		PrivacyURL: "https://doist.com/privacy", DocsURL: "https://developer.todoist.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "api_key", SelfService: true,
		Scopes: []string{"tasks:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a task in your Todoist account",
		},
		Actions: []addonAction{
			{ID: "create-task", Label: "Create Todoist task", Description: "Create a task from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiKey", Label: "Todoist API token", Type: "password", Required: true, Secret: true, Placeholder: "Paste your Todoist API token", Help: "Create a token in Todoist → Settings → Integrations → Developer. The token is encrypted before it is stored."},
		},
		SortOrder: 20,
	},
	{
		ID: "slack", Name: "Slack", Vendor: "Slack Technologies", Category: "Communication",
		Description: "Post a clean summary of an email into the Slack channel your workspace has connected.",
		Icon:        "i-people", Color: "#4a154b", LogoAsset: "slack.png",
		Tagline:    "Share an email summary with the right Slack channel.",
		WebsiteURL: "https://slack.com", SupportURL: "https://slack.com/help",
		PrivacyURL: "https://slack.com/privacy-policy", DocsURL: "https://api.slack.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "webhook", RequiresTenantSetup: true,
		Scopes: []string{"chat:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Post that summary to the configured Slack channel",
		},
		Actions: []addonAction{
			{ID: "send-to-channel", Label: "Send to Slack", Description: "Post the selected email summary to Slack.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "webhookUrl", Label: "Slack incoming webhook URL", Type: "password", Required: true, Secret: true, Placeholder: "https://hooks.slack.com/services/…", Help: "Create an Incoming Webhook in your Slack app, choose its channel, and paste the URL here. The URL is encrypted before it is stored."},
		},
		SortOrder: 30,
	},
	{
		ID: "hubspot", Name: "HubSpot", Vendor: "HubSpot, Inc.", Category: "CRM",
		Description: "Log the selected email to the matching HubSpot contact's CRM timeline.",
		Icon:        "i-people", Color: "#ff7a59", LogoAsset: "hubspot.svg",
		Tagline:    "Log an email to the matching contact's CRM timeline.",
		WebsiteURL: "https://www.hubspot.com", SupportURL: "https://help.hubspot.com",
		PrivacyURL: "https://legal.hubspot.com/privacy-policy", DocsURL: "https://developers.hubspot.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"crm.objects.contacts.read", "crm.objects.contacts.write", "crm.objects.notes.write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Look up the sender as a HubSpot contact",
			"Create a note on that contact's timeline",
		},
		Actions: []addonAction{
			{ID: "log-email", Label: "Log email to HubSpot", Description: "Create a HubSpot note on the sender's contact record.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "accessToken", Label: "HubSpot private app access token", Type: "password", Required: true, Secret: true, Placeholder: "pat-…", Help: "Create a private app with contact read/write and notes write scopes. The token is encrypted before it is stored."},
		},
		SortOrder: 40,
	},
	{
		ID: "asana", Name: "Asana", Vendor: "Asana, Inc.", Category: "Tasks",
		Description: "Create an Asana task from the selected email in the project your workspace connects.",
		Icon:        "i-check-circle", Color: "#f06a6a", LogoAsset: "asana.svg",
		Tagline:    "Create a project task from the email you are reading.",
		WebsiteURL: "https://asana.com", SupportURL: "https://asana.com/support",
		PrivacyURL: "https://asana.com/privacy", DocsURL: "https://developers.asana.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "api_key", SelfService: true,
		Scopes: []string{"tasks:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a task in your connected Asana project",
		},
		Actions: []addonAction{
			{ID: "create-task", Label: "Create Asana task", Description: "Create a task from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiKey", Label: "Asana personal access token", Type: "password", Required: true, Secret: true, Placeholder: "Asana PAT", Help: "Create a personal access token in Asana → Settings → Apps. The token is encrypted before it is stored."},
			{Key: "projectGid", Label: "Project GID", Type: "text", Required: true, Placeholder: "1201234567890123", Help: "Open the Asana project and copy the numeric ID from its URL or project settings."},
		},
		SortOrder: 50,
	},
	{
		ID: "pagerduty", Name: "PagerDuty", Vendor: "PagerDuty, Inc.", Category: "Operations",
		Description: "Raise a PagerDuty incident from the email you are reading, with the sender and preview attached.",
		Icon:        "i-alert", Color: "#06ac38", LogoAsset: "pagerduty.svg",
		Tagline:    "Raise an incident from the email that needs attention.",
		WebsiteURL: "https://www.pagerduty.com", SupportURL: "https://support.pagerduty.com",
		PrivacyURL: "https://www.pagerduty.com/privacy/", DocsURL: "https://developer.pagerduty.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"events:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Send a trigger event to the configured PagerDuty routing key",
		},
		Actions: []addonAction{
			{ID: "trigger-incident", Label: "Trigger PagerDuty incident", Description: "Send an incident trigger from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "routingKey", Label: "PagerDuty integration routing key", Type: "password", Required: true, Secret: true, Placeholder: "Routing key", Help: "Create an Events API v2 integration in PagerDuty and paste its routing key. The key is encrypted before it is stored."},
			{Key: "severity", Label: "Default severity", Type: "select", Default: "error", Required: true, Options: []addonFieldOption{
				{Value: "critical", Label: "Critical"}, {Value: "error", Label: "Error"},
				{Value: "warning", Label: "Warning"}, {Value: "info", Label: "Info"},
			}},
		},
		SortOrder: 60,
	},
	{
		ID: "zoom", Name: "Zoom", Vendor: "Zoom Communications", Category: "Meetings",
		Description: "Create a Zoom meeting link from an email and copy it into the conversation.",
		Icon:        "i-calendar-plus", Color: "#0b5cff", LogoAsset: "zoom.svg",
		Tagline:    "Create a meeting link from any conversation.",
		WebsiteURL: "https://zoom.us", SupportURL: "https://support.zoom.us",
		PrivacyURL: "https://zoom.us/privacy", DocsURL: "https://developers.zoom.us",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "oauth2", RequiresTenantSetup: true,
		Scopes: []string{"meeting:write"},
		Permissions: []string{
			"Read the selected email's subject",
			"Create a Zoom meeting under the connected account",
		},
		Actions: []addonAction{
			{ID: "create-meeting", Label: "Create Zoom meeting", Description: "Create a meeting link for the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "accountId", Label: "Zoom account ID", Type: "text", Required: true, Placeholder: "Zoom account ID"},
			{Key: "clientId", Label: "Zoom client ID", Type: "text", Required: true, Placeholder: "Zoom app client ID"},
			{Key: "clientSecret", Label: "Zoom client secret", Type: "password", Required: true, Secret: true, Placeholder: "Zoom app client secret"},
			{Key: "duration", Label: "Default meeting length", Type: "select", Default: "30", Required: true, Options: []addonFieldOption{
				{Value: "30", Label: "30 minutes"}, {Value: "45", Label: "45 minutes"}, {Value: "60", Label: "60 minutes"},
			}},
		},
		SortOrder: 70,
	},
	{
		ID: "pipedrive", Name: "Pipedrive", Vendor: "Pipedrive, Inc.", Category: "CRM",
		Description: "Log the selected email to the matching person in Pipedrive.",
		Tagline:     "Keep every customer email attached to the right Pipedrive record.",
		Icon:        "i-people", Color: "#017737", LogoAsset: "pipedrive.png",
		WebsiteURL: "https://www.pipedrive.com", SupportURL: "https://support.pipedrive.com",
		PrivacyURL: "https://www.pipedrive.com/en/privacy", DocsURL: "https://developers.pipedrive.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"contacts:read", "notes:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Find the sender as a Pipedrive person",
			"Create a note on that person's timeline",
		},
		Actions: []addonAction{
			{ID: "log-email", Label: "Log email to Pipedrive", Description: "Create a note on the sender's Pipedrive person.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiToken", Label: "Pipedrive API token", Type: "password", Required: true, Secret: true, Placeholder: "Pipedrive API token", Help: "Create a personal API token in Pipedrive → Settings → Personal preferences → API. The token is encrypted before it is stored."},
		},
		SortOrder: 80,
	},
	{
		ID: "zoho", Name: "Zoho CRM", Vendor: "Zoho Corporation", Category: "CRM",
		Description: "Add an email note to the matching contact in Zoho CRM.",
		Tagline:     "Keep customer conversations in the Zoho CRM contact timeline.",
		Icon:        "i-people", Color: "#e42527", LogoAsset: "zoho.svg",
		WebsiteURL: "https://www.zoho.com/crm/", SupportURL: "https://help.zoho.com/portal/en/kb/crm",
		PrivacyURL: "https://www.zoho.com/privacy.html", DocsURL: "https://www.zoho.com/crm/developer/docs/api/",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "oauth2", RequiresTenantSetup: true,
		Scopes: []string{"ZohoCRM.modules.contacts.READ", "ZohoCRM.modules.notes.CREATE"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Find the sender as a Zoho CRM contact",
			"Create a note on that contact's record",
		},
		Actions: []addonAction{
			{ID: "log-email", Label: "Log email to Zoho CRM", Description: "Create a note on the sender's Zoho CRM contact record.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "region", Label: "Zoho data centre", Type: "select", Default: "com", Required: true, Options: []addonFieldOption{
				{Value: "com", Label: "United States (.com)"}, {Value: "eu", Label: "Europe (.eu)"},
				{Value: "in", Label: "India (.in)"}, {Value: "com.au", Label: "Australia (.com.au)"},
				{Value: "jp", Label: "Japan (.jp)"},
			}},
			{Key: "clientId", Label: "Zoho client ID", Type: "text", Required: true, Placeholder: "1000.XXXXXXXXXXXXXXXX"},
			{Key: "clientSecret", Label: "Zoho client secret", Type: "password", Required: true, Secret: true, Placeholder: "Zoho client secret"},
			{Key: "refreshToken", Label: "Zoho refresh token", Type: "password", Required: true, Secret: true, Placeholder: "Refresh token from a Self Client", Help: "Create a Self Client in the Zoho API console, generate a refresh token with the CRM scopes, and paste it here. The token is encrypted before it is stored."},
		},
		SortOrder: 90,
	},
	{
		ID: "clickup", Name: "ClickUp", Vendor: "ClickUp", Category: "Tasks",
		Description: "Create a ClickUp task from the email you are reading.",
		Tagline:     "Turn important mail into an actionable ClickUp task.",
		Icon:        "i-check-circle", Color: "#7b68ee", LogoAsset: "clickup.svg",
		WebsiteURL: "https://clickup.com", SupportURL: "https://help.clickup.com",
		PrivacyURL: "https://clickup.com/privacy", DocsURL: "https://developer.clickup.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "api_key", SelfService: true,
		Scopes: []string{"tasks:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a task in the connected ClickUp list",
		},
		Actions: []addonAction{
			{ID: "create-task", Label: "Create ClickUp task", Description: "Create a task from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiToken", Label: "ClickUp personal API token", Type: "password", Required: true, Secret: true, Placeholder: "pk_…", Help: "Create a personal API token in ClickUp → Settings → Apps. The token is encrypted before it is stored."},
			{Key: "listId", Label: "ClickUp list ID", Type: "text", Required: true, Placeholder: "901234567890", Help: "Open the ClickUp list and copy the numeric ID from its URL or list settings."},
		},
		SortOrder: 100,
	},
	{
		ID: "zendesk", Name: "Zendesk", Vendor: "Zendesk, Inc.", Category: "Support",
		Description: "Open a support ticket from the email you are reading.",
		Tagline:     "Turn customer mail into a tracked Zendesk ticket.",
		Icon:        "i-people", Color: "#03363d", LogoAsset: "zendesk.svg",
		WebsiteURL: "https://www.zendesk.com", SupportURL: "https://support.zendesk.com",
		PrivacyURL: "https://www.zendesk.com/legal/privacy-policy/", DocsURL: "https://developer.zendesk.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"tickets:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a ticket with the email as the initial comment",
		},
		Actions: []addonAction{
			{ID: "create-ticket", Label: "Create Zendesk ticket", Description: "Open a ticket with the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "subdomain", Label: "Zendesk subdomain", Type: "text", Required: true, Placeholder: "acme", Help: "Use acme when your help centre is acme.zendesk.com."},
			{Key: "email", Label: "Agent email", Type: "text", Required: true, Placeholder: "support@example.com", Help: "The Zendesk agent email that owns the API token."},
			{Key: "apiToken", Label: "Zendesk API token", Type: "password", Required: true, Secret: true, Placeholder: "Zendesk API token", Help: "Create an API token in Zendesk Admin Center → Apps and integrations → APIs → Zendesk API. The token is encrypted before it is stored."},
		},
		SortOrder: 110,
	},
	{
		ID: "salesforce", Name: "Salesforce", Vendor: "Salesforce, Inc.", Category: "CRM",
		Description: "Log the selected email as a completed task on the matching Salesforce contact.",
		Tagline:     "Keep every customer conversation in Salesforce.",
		Icon:        "i-people", Color: "#00a1e0", LogoAsset: "salesforce.svg",
		WebsiteURL: "https://www.salesforce.com", SupportURL: "https://help.salesforce.com",
		PrivacyURL: "https://www.salesforce.com/company/privacy/", DocsURL: "https://developer.salesforce.com/docs",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "oauth2", RequiresTenantSetup: true,
		Scopes: []string{"api", "refresh_token"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Find the sender as a Salesforce contact",
			"Create a completed task on that contact",
		},
		Actions: []addonAction{
			{ID: "log-email", Label: "Log email to Salesforce", Description: "Create a task on the sender's Salesforce contact.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "loginUrl", Label: "Salesforce login", Type: "select", Default: "https://login.salesforce.com", Required: true, Options: []addonFieldOption{
				{Value: "https://login.salesforce.com", Label: "Production"}, {Value: "https://test.salesforce.com", Label: "Sandbox"},
			}},
			{Key: "clientId", Label: "Connected app consumer key", Type: "text", Required: true, Placeholder: "Consumer key"},
			{Key: "clientSecret", Label: "Connected app consumer secret", Type: "password", Required: true, Secret: true, Placeholder: "Consumer secret"},
			{Key: "refreshToken", Label: "Salesforce refresh token", Type: "password", Required: true, Secret: true, Placeholder: "Refresh token", Help: "Authorize a connected app with the api and refresh_token scopes, then paste its refresh token. The token is encrypted before it is stored."},
		},
		SortOrder: 120,
	},
	{
		ID: "freshdesk", Name: "Freshdesk", Vendor: "Freshworks", Category: "Support",
		Description: "Open a Freshdesk ticket from the email you are reading.",
		Tagline:     "Turn customer mail into a Freshdesk ticket.",
		Icon:        "i-people", Color: "#25c16f", LogoAsset: "freshdesk.svg",
		WebsiteURL: "https://www.freshworks.com/freshdesk/", SupportURL: "https://support.freshdesk.com",
		PrivacyURL: "https://www.freshworks.com/privacy/", DocsURL: "https://developers.freshdesk.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"tickets:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a ticket with the email as the initial description",
		},
		Actions: []addonAction{
			{ID: "create-ticket", Label: "Create Freshdesk ticket", Description: "Open a ticket with the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "domain", Label: "Freshdesk domain", Type: "text", Required: true, Placeholder: "acme", Help: "Use acme when your help desk is acme.freshdesk.com."},
			{Key: "apiKey", Label: "Freshdesk API key", Type: "password", Required: true, Secret: true, Placeholder: "Freshdesk API key", Help: "Find your API key in Freshdesk Profile settings. The key is encrypted before it is stored."},
		},
		SortOrder: 130,
	},
	{
		ID: "intercom", Name: "Intercom", Vendor: "Intercom, Inc.", Category: "Support",
		Description: "Add an internal note to the matching Intercom contact.",
		Tagline:     "Keep customer context in Intercom without copying and pasting.",
		Icon:        "i-people", Color: "#1f8ded", LogoAsset: "intercom.svg",
		WebsiteURL: "https://www.intercom.com", SupportURL: "https://help.intercom.com",
		PrivacyURL: "https://www.intercom.com/legal/privacy", DocsURL: "https://developers.intercom.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"contacts:read", "notes:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Find the sender as an Intercom contact",
			"Add an internal note to that contact",
		},
		Actions: []addonAction{
			{ID: "add-note", Label: "Add Intercom note", Description: "Add a note to the sender's Intercom contact.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "region", Label: "Intercom region", Type: "select", Default: "us", Required: true, Options: []addonFieldOption{
				{Value: "us", Label: "United States"}, {Value: "eu", Label: "Europe"}, {Value: "au", Label: "Australia"},
			}},
			{Key: "accessToken", Label: "Intercom access token", Type: "password", Required: true, Secret: true, Placeholder: "Intercom access token", Help: "Create a private app access token in Intercom → Settings → Integrations. The token is encrypted before it is stored."},
		},
		SortOrder: 140,
	},
	{
		ID: "ticktick", Name: "TickTick", Vendor: "TickTick", Category: "Tasks",
		Description: "Create a TickTick task from the email you are reading.",
		Tagline:     "Capture an email as a TickTick task.",
		Icon:        "i-check-circle", Color: "#4772fa", LogoAsset: "ticktick.svg",
		WebsiteURL: "https://ticktick.com", SupportURL: "https://help.ticktick.com",
		PrivacyURL: "https://ticktick.com/privacy", DocsURL: "https://developer.ticktick.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "oauth2", SelfService: true,
		Scopes: []string{"tasks:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create a task in the connected TickTick project",
		},
		Actions: []addonAction{
			{ID: "create-task", Label: "Create TickTick task", Description: "Create a task from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "accessToken", Label: "TickTick access token", Type: "password", Required: true, Secret: true, Placeholder: "TickTick OAuth access token", Help: "Create an app in the TickTick developer console and paste its OAuth access token. The token is encrypted before it is stored."},
			{Key: "projectId", Label: "TickTick project ID", Type: "text", Required: true, Placeholder: "Project ID", Help: "The project ID where new tasks should be created."},
		},
		SortOrder: 150,
	},
	{
		ID: "calendly", Name: "Calendly", Vendor: "Calendly LLC", Category: "Scheduling",
		Description: "Create a single-use Calendly booking link from an email.",
		Tagline:     "Offer a booking link without leaving the conversation.",
		Icon:        "i-calendar-plus", Color: "#006bff", LogoAsset: "calendly.svg",
		WebsiteURL: "https://calendly.com", SupportURL: "https://help.calendly.com",
		PrivacyURL: "https://calendly.com/privacy", DocsURL: "https://developer.calendly.com",
		ConnectorVersion: "1.0",
		ConnectionScope:  "user", AuthType: "api_key", SelfService: true,
		Scopes: []string{"scheduling_links:write"},
		Permissions: []string{
			"Read the selected email's subject and sender",
			"Create a single-use scheduling link for your Calendly event",
		},
		Actions: []addonAction{
			{ID: "create-link", Label: "Create Calendly link", Description: "Create a single-use booking link for this email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "accessToken", Label: "Calendly personal access token", Type: "password", Required: true, Secret: true, Placeholder: "Calendly access token", Help: "Create a personal access token in Calendly integrations. The token is encrypted before it is stored."},
			{Key: "eventTypeUri", Label: "Calendly event type URI", Type: "text", Required: true, Placeholder: "https://api.calendly.com/event_types/…"},
			{Key: "ownerUri", Label: "Calendly owner URI", Type: "text", Required: true, Placeholder: "https://api.calendly.com/users/…"},
		},
		SortOrder: 160,
	},
	{
		ID: "jira", Name: "Jira", Vendor: "Atlassian", Category: "Engineering",
		Description: "Create a Jira issue from the email you are reading.",
		Tagline:     "Turn customer mail into a tracked Jira issue.",
		Icon:        "i-check-circle", Color: "#0052cc", LogoAsset: "jira.svg",
		WebsiteURL: "https://www.atlassian.com/software/jira", SupportURL: "https://support.atlassian.com/jira-software-cloud/",
		PrivacyURL: "https://www.atlassian.com/legal/privacy-policy", DocsURL: "https://developer.atlassian.com/cloud/jira/platform/rest/v3/",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"issues:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create an issue in the configured Jira project",
		},
		Actions: []addonAction{
			{ID: "create-issue", Label: "Create Jira issue", Description: "Create an issue from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "site", Label: "Jira Cloud site", Type: "text", Required: true, Placeholder: "acme.atlassian.net", Help: "Use the hostname of your Jira Cloud site, without https://."},
			{Key: "email", Label: "Atlassian account email", Type: "text", Required: true, Placeholder: "you@example.com"},
			{Key: "apiToken", Label: "Atlassian API token", Type: "password", Required: true, Secret: true, Placeholder: "Atlassian API token", Help: "Create an API token in your Atlassian account security settings. The token is encrypted before it is stored."},
			{Key: "projectKey", Label: "Jira project key", Type: "text", Required: true, Placeholder: "ENG"},
			{Key: "issueType", Label: "Issue type", Type: "text", Default: "Task", Required: true, Placeholder: "Task"},
		},
		SortOrder: 170,
	},
	{
		ID: "linear", Name: "Linear", Vendor: "Linear", Category: "Engineering",
		Description: "Create a Linear issue from the email you are reading.",
		Tagline:     "Move an email into the team's Linear backlog.",
		Icon:        "i-check-circle", Color: "#5e6ad2", LogoAsset: "linear.svg",
		WebsiteURL: "https://linear.app", SupportURL: "https://linear.app/contact/support",
		PrivacyURL: "https://linear.app/privacy", DocsURL: "https://developers.linear.app",
		ConnectorVersion: "1.0",
		ConnectionScope:  "tenant", AuthType: "api_key", RequiresTenantSetup: true,
		Scopes: []string{"issues:write"},
		Permissions: []string{
			"Read the selected email's subject, sender and preview",
			"Create an issue in the configured Linear team",
		},
		Actions: []addonAction{
			{ID: "create-issue", Label: "Create Linear issue", Description: "Create an issue from the selected email.", Surface: "reader"},
		},
		Fields: []addonField{
			{Key: "apiKey", Label: "Linear API key", Type: "password", Required: true, Secret: true, Placeholder: "lin_api_…", Help: "Create a personal API key in Linear → Settings → API. The key is encrypted before it is stored."},
			{Key: "teamId", Label: "Linear team ID", Type: "text", Required: true, Placeholder: "Team UUID", Help: "Find the team ID in Linear team settings or the API."},
		},
		SortOrder: 180,
	},
}

func addonDefinitionByID(id string) (addonDefinition, bool) {
	for _, definition := range addonDefinitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return addonDefinition{}, false
}

func addonLogoURL(asset string) string {
	asset = strings.TrimSpace(asset)
	if asset == "" {
		return ""
	}
	return "/addon-logos/" + asset
}

func (s *Server) ensureAddonCatalog(ctx context.Context) error {
	for _, definition := range addonDefinitions {
		scopes, _ := json.Marshal(definition.Scopes)
		permissions, _ := json.Marshal(definition.Permissions)
		actions, _ := json.Marshal(definition.Actions)
		fields, _ := json.Marshal(definition.Fields)
		_, err := s.store.DB.Exec(ctx, `
			INSERT INTO addon_system_catalog (
				id, name, vendor, category, description, tagline, icon, color, logo_asset,
				website_url, support_url, privacy_url, docs_url, connector_version,
				connection_scope, auth_type, self_service, requires_tenant_setup,
				scopes, permissions, actions, fields, sort_order
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
			ON CONFLICT (id) DO UPDATE SET
				name=EXCLUDED.name, vendor=EXCLUDED.vendor, category=EXCLUDED.category,
				description=EXCLUDED.description, tagline=EXCLUDED.tagline,
				icon=EXCLUDED.icon, color=EXCLUDED.color, logo_asset=EXCLUDED.logo_asset,
				website_url=EXCLUDED.website_url, support_url=EXCLUDED.support_url,
				privacy_url=EXCLUDED.privacy_url, docs_url=EXCLUDED.docs_url,
				connector_version=EXCLUDED.connector_version,
				connection_scope=EXCLUDED.connection_scope, auth_type=EXCLUDED.auth_type,
				self_service=EXCLUDED.self_service, requires_tenant_setup=EXCLUDED.requires_tenant_setup,
				scopes=EXCLUDED.scopes, permissions=EXCLUDED.permissions, actions=EXCLUDED.actions,
				fields=EXCLUDED.fields, sort_order=EXCLUDED.sort_order, updated_at=now()
		`, definition.ID, definition.Name, definition.Vendor, definition.Category, definition.Description,
			definition.Tagline, definition.Icon, definition.Color, definition.LogoAsset,
			definition.WebsiteURL, definition.SupportURL, definition.PrivacyURL, definition.DocsURL,
			definition.ConnectorVersion, definition.ConnectionScope, definition.AuthType,
			definition.SelfService, definition.RequiresTenantSetup, scopes, permissions, actions,
			fields, definition.SortOrder)
		if err != nil {
			return err
		}
	}
	return nil
}

type addonConnectionRecord struct {
	ID           string
	OwnerID      string
	AccountLabel string
	Sealed       string
	Config       map[string]any
}

func (s *Server) addonActiveConnection(ctx context.Context, tenantID, addonID, userID, scope string) (addonConnectionRecord, bool) {
	var record addonConnectionRecord
	var rawConfig []byte
	err := s.store.DB.QueryRow(ctx, `
		SELECT id, COALESCE(owner_user_id::text, ''), account_label, credentials_sealed, config
		FROM addon_connections
		WHERE tenant_id = $1 AND addon_id = $2 AND connection_scope = $3
		  AND status = 'active' AND ($3 = 'tenant' OR owner_user_id = $4)
		ORDER BY updated_at DESC LIMIT 1
	`, tenantID, addonID, scope, userID).Scan(&record.ID, &record.OwnerID, &record.AccountLabel, &record.Sealed, &rawConfig)
	if err != nil {
		return addonConnectionRecord{}, false
	}
	record.Config = map[string]any{}
	if len(rawConfig) > 0 {
		_ = json.Unmarshal(rawConfig, &record.Config)
	}
	if strings.TrimSpace(record.Sealed) == "" {
		return addonConnectionRecord{}, false
	}
	return record, true
}

func (s *Server) addonInstallActive(ctx context.Context, tenantID, addonID, userID string) (bool, time.Time) {
	var created time.Time
	err := s.store.DB.QueryRow(ctx, `
		SELECT created_at FROM addon_installs
		WHERE tenant_id = $1 AND addon_id = $2 AND user_id = $3 AND status = 'active'
	`, tenantID, addonID, userID).Scan(&created)
	return err == nil, created
}

type addonTenantRecord struct {
	Exists              bool
	Enabled             bool
	InstallPolicy       string
	Config              map[string]any
	ClientID            string
	ClientSecretSealed  string
	WebhookSecretSealed string
}

func (s *Server) addonTenantRecord(ctx context.Context, tenantID, addonID string) addonTenantRecord {
	record := addonTenantRecord{InstallPolicy: "self_service", Config: map[string]any{}}
	var rawConfig []byte
	err := s.store.DB.QueryRow(ctx, `
		SELECT enabled, install_policy, config, client_id, client_secret_sealed, webhook_secret_sealed
		FROM tenant_addons WHERE tenant_id = $1 AND addon_id = $2
	`, tenantID, addonID).Scan(&record.Enabled, &record.InstallPolicy, &rawConfig, &record.ClientID, &record.ClientSecretSealed, &record.WebhookSecretSealed)
	if err != nil {
		return record
	}
	record.Exists = true
	if record.InstallPolicy == "" {
		record.InstallPolicy = "self_service"
	}
	if len(rawConfig) > 0 {
		_ = json.Unmarshal(rawConfig, &record.Config)
	}
	return record
}

func (s *Server) addonRequestPending(ctx context.Context, tenantID, addonID, userID string) (bool, string) {
	var requestType string
	err := s.store.DB.QueryRow(ctx, `
		SELECT request_type FROM addon_requests
		WHERE tenant_id = $1 AND addon_id = $2 AND user_id = $3 AND status = 'pending'
		ORDER BY created_at DESC LIMIT 1
	`, tenantID, addonID, userID).Scan(&requestType)
	if err != nil {
		return false, ""
	}
	return true, requestType
}

func (s *Server) addonSystemEnabled(ctx context.Context, addonID string) bool {
	var enabled bool
	err := s.store.DB.QueryRow(ctx, `SELECT enabled FROM addon_system_catalog WHERE id = $1`, addonID).Scan(&enabled)
	return err == nil && enabled
}

func addonEffectiveTenantEnabled(scope string, exists, enabled bool) bool {
	if !exists {
		return scope == "user"
	}
	return enabled
}

func addonConnectionPublicConfig(definition addonDefinition, userConn, tenantConn addonConnectionRecord) map[string]any {
	result := map[string]any{}
	source := userConn.Config
	if definition.ConnectionScope == "tenant" {
		source = tenantConn.Config
	}
	for key, value := range source {
		if strings.EqualFold(key, "password") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "token") {
			continue
		}
		result[key] = value
	}
	return result
}

func (s *Server) addonView(ctx context.Context, sess *sessionRecord, definition addonDefinition, admin bool) map[string]any {
	systemEnabled := s.addonSystemEnabled(ctx, definition.ID)
	tenant := s.addonTenantRecord(ctx, sess.User.TenantID, definition.ID)
	effectiveTenant := addonEffectiveTenantEnabled(definition.ConnectionScope, tenant.Exists, tenant.Enabled)
	userConn, userConfigured := s.addonActiveConnection(ctx, sess.User.TenantID, definition.ID, sess.User.ID, "user")
	tenantConn, tenantConfigured := s.addonActiveConnection(ctx, sess.User.TenantID, definition.ID, sess.User.ID, "tenant")
	configured := userConfigured
	connectionLabel := userConn.AccountLabel
	if definition.ConnectionScope == "tenant" {
		configured = tenantConfigured
		connectionLabel = tenantConn.AccountLabel
	}
	installed, installedAt := s.addonInstallActive(ctx, sess.User.TenantID, definition.ID, sess.User.ID)
	requestPending, requestType := s.addonRequestPending(ctx, sess.User.TenantID, definition.ID, sess.User.ID)
	approvalRequired := !admin && tenant.InstallPolicy == "approval_required"

	tenantPolicyDisabled := tenant.Exists && !tenant.Enabled
	status := "blocked"
	switch {
	case !systemEnabled || tenantPolicyDisabled:
		status = "blocked"
	case installed && configured:
		status = "installed"
	case installed:
		status = "needs_setup"
	case configured && approvalRequired:
		status = "approval_required"
	case configured:
		status = "ready"
	case definition.ConnectionScope == "user" && definition.SelfService:
		status = "needs_connection"
	default:
		status = "needs_setup"
	}
	if requestPending && status != "installed" && status != "ready" {
		status = "requested"
	}
	canRequest := !admin && systemEnabled && (status == "needs_setup" || status == "approval_required" || (status == "blocked" && tenantPolicyDisabled))
	installedAtValue := any(nil)
	if installed && !installedAt.IsZero() {
		installedAtValue = installedAt.UTC().Format(time.RFC3339)
	}
	availableActions := []addonAction{}
	if installed && configured && systemEnabled && effectiveTenant {
		availableActions = definition.Actions
	}
	return map[string]any{
		"id": definition.ID, "name": definition.Name, "vendor": definition.Vendor,
		"category": definition.Category, "description": definition.Description,
		"tagline": definition.Tagline, "icon": definition.Icon, "color": definition.Color,
		"logo":       addonLogoURL(definition.LogoAsset),
		"websiteUrl": definition.WebsiteURL, "supportUrl": definition.SupportURL,
		"privacyUrl": definition.PrivacyURL, "docsUrl": definition.DocsURL,
		"connectorVersion": definition.ConnectorVersion,
		"connectionScope":  definition.ConnectionScope, "authType": definition.AuthType,
		"selfService": definition.SelfService, "requiresTenantSetup": definition.RequiresTenantSetup,
		"scopes": definition.Scopes, "permissions": definition.Permissions,
		"actions": definition.Actions, "fields": definition.Fields,
		"systemEnabled": systemEnabled, "tenantEnabled": effectiveTenant,
		"tenantPolicySet": tenant.Exists, "installPolicy": tenant.InstallPolicy,
		"approvalRequired": approvalRequired,
		"oauthAvailable":   definition.ID == "slack", "oauthMode": s.addonOAuthMode(definition.ID),
		"configured": configured, "connectionLabel": connectionLabel,
		"connectionConfig": addonConnectionPublicConfig(definition, userConn, tenantConn),
		"installed":        installed, "installedAt": installedAtValue,
		"requestPending": requestPending, "requestType": requestType, "status": status,
		"canRequest": canRequest, "canManage": admin,
		"availableActions": availableActions,
	}
}

func (s *Server) getAddons(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if err := s.ensureAddonCatalog(r.Context()); err != nil {
		s.logger.Error("addon catalog", "err", err)
		writeError(w, http.StatusInternalServerError, "ADDONS", "Could not load add-ons")
		return
	}
	admin := webmailAdminRole(sess.User.Role)
	addons := make([]map[string]any, 0, len(addonDefinitions))
	for _, definition := range addonDefinitions {
		if !s.addonSystemEnabled(r.Context(), definition.ID) {
			continue
		}
		addons = append(addons, s.addonView(r.Context(), sess, definition, admin))
	}
	requests := []map[string]any{}
	if admin {
		rows, err := s.store.DB.Query(r.Context(), `
			SELECT r.id, r.addon_id, a.name, a.logo_asset, r.user_id::text, u.name, u.email::text,
			       r.request_type, r.note, r.created_at
			FROM addon_requests r
			JOIN addon_system_catalog a ON a.id = r.addon_id
			JOIN users u ON u.id = r.user_id
			WHERE r.tenant_id = $1 AND r.status = 'pending'
			ORDER BY r.created_at ASC
		`, sess.User.TenantID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, addonID, addonName, addonLogo, userID, userName, userEmail, requestType, note string
				var created time.Time
				if rows.Scan(&id, &addonID, &addonName, &addonLogo, &userID, &userName, &userEmail, &requestType, &note, &created) == nil {
					requests = append(requests, map[string]any{
						"id": id, "addonId": addonID, "addonName": addonName,
						"addonLogo": addonLogoURL(addonLogo),
						"userId":    userID, "userName": userName, "userEmail": userEmail,
						"requestType": requestType, "note": note,
						"createdAt": created.UTC().Format(time.RFC3339),
					})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"addons": addons, "isAdmin": admin, "requests": requests,
		"pendingRequestCount": len(requests),
	})
}

func (s *Server) connectAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if err := s.ensureAddonCatalog(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ADDONS", "Could not load add-ons")
		return
	}
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	if !s.addonSystemEnabled(r.Context(), definition.ID) {
		writeError(w, http.StatusForbidden, "ADDON_DISABLED", "This add-on is not enabled on this system")
		return
	}
	admin := webmailAdminRole(sess.User.Role)
	if definition.ConnectionScope == "tenant" && !admin {
		writeError(w, http.StatusForbidden, "ADDON_ADMIN", "Only an administrator can set up this add-on for the workspace")
		return
	}
	if definition.ConnectionScope == "user" && !admin {
		tenant := s.addonTenantRecord(r.Context(), sess.User.TenantID, definition.ID)
		if tenant.Exists && !tenant.Enabled {
			writeError(w, http.StatusForbidden, "ADDON_DISABLED", "This add-on is disabled for the workspace")
			return
		}
	}
	var body map[string]string
	if err := decodeJSON(w, r, &body, 64*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	credentials := map[string]string{}
	config := map[string]any{}
	accountLabel := definition.Name
	switch definition.ID {
	case "slack":
		webhookURL := strings.TrimSpace(body["webhookUrl"])
		if err := validateSlackWebhook(webhookURL); err != nil {
			writeError(w, http.StatusBadRequest, "ADDON_WEBHOOK", err.Error())
			return
		}
		credentials["webhookUrl"] = webhookURL
		accountLabel = "Slack channel"
	case "todoist":
		apiKey := strings.TrimSpace(body["apiKey"])
		if len(apiKey) < 8 || len(apiKey) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Todoist API token")
			return
		}
		credentials["apiKey"] = apiKey
		accountLabel = "Todoist account"
	case "deepl":
		apiKey := strings.TrimSpace(body["apiKey"])
		if len(apiKey) < 8 || len(apiKey) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid DeepL API key")
			return
		}
		target := strings.ToUpper(strings.TrimSpace(body["targetLanguage"]))
		if target == "" {
			target = "EN-US"
		}
		if !allowedDeepLTarget(target) {
			writeError(w, http.StatusBadRequest, "ADDON_LANGUAGE", "Choose a supported DeepL target language")
			return
		}
		credentials["apiKey"] = apiKey
		config["targetLanguage"] = target
		accountLabel = "DeepL account"
	case "hubspot":
		token := strings.TrimSpace(body["accessToken"])
		if len(token) < 12 || len(token) > 4000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a HubSpot private app access token")
			return
		}
		credentials["accessToken"] = token
		accountLabel = "HubSpot portal"
	case "asana":
		token := strings.TrimSpace(body["apiKey"])
		if len(token) < 12 || len(token) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Asana personal access token")
			return
		}
		project := strings.TrimSpace(body["projectGid"])
		if !validAsanaGID(project) {
			writeError(w, http.StatusBadRequest, "ADDON_PROJECT", "Enter a valid Asana project GID")
			return
		}
		credentials["apiKey"] = token
		config["projectGid"] = project
		accountLabel = "Asana account"
	case "pagerduty":
		routingKey := strings.TrimSpace(body["routingKey"])
		if len(routingKey) < 8 || len(routingKey) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid PagerDuty integration routing key")
			return
		}
		severity := strings.ToLower(strings.TrimSpace(body["severity"]))
		if severity == "" {
			severity = "error"
		}
		if !validPagerDutySeverity(severity) {
			writeError(w, http.StatusBadRequest, "ADDON_SEVERITY", "Choose a valid PagerDuty severity")
			return
		}
		credentials["routingKey"] = routingKey
		config["severity"] = severity
		accountLabel = "PagerDuty integration"
	case "zoom":
		accountID := strings.TrimSpace(body["accountId"])
		clientID := strings.TrimSpace(body["clientId"])
		clientSecret := strings.TrimSpace(body["clientSecret"])
		if accountID == "" || clientID == "" || clientSecret == "" {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Enter the Zoom account ID, client ID and client secret")
			return
		}
		duration := strings.TrimSpace(body["duration"])
		if duration == "" {
			duration = "30"
		}
		if duration != "30" && duration != "45" && duration != "60" {
			writeError(w, http.StatusBadRequest, "ADDON_DURATION", "Choose a supported Zoom meeting length")
			return
		}
		credentials["accountId"] = accountID
		credentials["clientId"] = clientID
		credentials["clientSecret"] = clientSecret
		config["duration"] = duration
		accountLabel = "Zoom account"
	case "pipedrive":
		apiToken := strings.TrimSpace(body["apiToken"])
		if len(apiToken) < 8 || len(apiToken) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Pipedrive API token")
			return
		}
		credentials["apiToken"] = apiToken
		accountLabel = "Pipedrive company"
	case "zoho":
		region := strings.ToLower(strings.TrimSpace(body["region"]))
		if region == "" {
			region = "com"
		}
		if !validZohoRegion(region) {
			writeError(w, http.StatusBadRequest, "ADDON_REGION", "Choose a supported Zoho data centre")
			return
		}
		clientID := strings.TrimSpace(body["clientId"])
		clientSecret := strings.TrimSpace(body["clientSecret"])
		refreshToken := strings.TrimSpace(body["refreshToken"])
		if clientID == "" || clientSecret == "" || refreshToken == "" {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Enter the Zoho client ID, client secret and refresh token")
			return
		}
		credentials["clientId"] = clientID
		credentials["clientSecret"] = clientSecret
		credentials["refreshToken"] = refreshToken
		config["region"] = region
		accountLabel = "Zoho CRM · " + region
	case "clickup":
		apiToken := strings.TrimSpace(body["apiToken"])
		if len(apiToken) < 8 || len(apiToken) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid ClickUp personal API token")
			return
		}
		listID := strings.TrimSpace(body["listId"])
		if !validClickUpList(listID) {
			writeError(w, http.StatusBadRequest, "ADDON_LIST", "Enter a valid ClickUp list ID")
			return
		}
		credentials["apiToken"] = apiToken
		config["listId"] = listID
		accountLabel = "ClickUp list"
	case "zendesk":
		subdomain := strings.ToLower(strings.TrimSpace(body["subdomain"]))
		if !validZendeskSubdomain(subdomain) {
			writeError(w, http.StatusBadRequest, "ADDON_SUBDOMAIN", "Enter a valid Zendesk subdomain")
			return
		}
		email := strings.TrimSpace(body["email"])
		if !looksLikeEmail(email) {
			writeError(w, http.StatusBadRequest, "ADDON_EMAIL", "Enter the Zendesk agent email")
			return
		}
		apiToken := strings.TrimSpace(body["apiToken"])
		if len(apiToken) < 8 || len(apiToken) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Zendesk API token")
			return
		}
		credentials["email"] = email
		credentials["apiToken"] = apiToken
		config["subdomain"] = subdomain
		accountLabel = subdomain + ".zendesk.com"
	case "salesforce":
		loginURL := strings.TrimSpace(body["loginUrl"])
		if loginURL == "" {
			loginURL = "https://login.salesforce.com"
		}
		if !validSalesforceLogin(loginURL) {
			writeError(w, http.StatusBadRequest, "ADDON_LOGIN", "Choose a valid Salesforce login URL")
			return
		}
		clientID := strings.TrimSpace(body["clientId"])
		clientSecret := strings.TrimSpace(body["clientSecret"])
		refreshToken := strings.TrimSpace(body["refreshToken"])
		if clientID == "" || clientSecret == "" || refreshToken == "" {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Enter the Salesforce consumer key, consumer secret and refresh token")
			return
		}
		credentials["clientId"] = clientID
		credentials["clientSecret"] = clientSecret
		credentials["refreshToken"] = refreshToken
		config["loginUrl"] = loginURL
		accountLabel = "Salesforce org"
	case "freshdesk":
		domain := strings.ToLower(strings.TrimSpace(body["domain"]))
		if !validZendeskSubdomain(domain) {
			writeError(w, http.StatusBadRequest, "ADDON_SUBDOMAIN", "Enter a valid Freshdesk domain")
			return
		}
		apiKey := strings.TrimSpace(body["apiKey"])
		if len(apiKey) < 8 || len(apiKey) > 500 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Freshdesk API key")
			return
		}
		credentials["apiKey"] = apiKey
		config["domain"] = domain
		accountLabel = domain + ".freshdesk.com"
	case "intercom":
		region := strings.ToLower(strings.TrimSpace(body["region"]))
		if region == "" {
			region = "us"
		}
		if !validIntercomRegion(region) {
			writeError(w, http.StatusBadRequest, "ADDON_REGION", "Choose a supported Intercom region")
			return
		}
		accessToken := strings.TrimSpace(body["accessToken"])
		if len(accessToken) < 8 || len(accessToken) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Intercom access token")
			return
		}
		credentials["accessToken"] = accessToken
		config["region"] = region
		accountLabel = "Intercom workspace"
	case "ticktick":
		accessToken := strings.TrimSpace(body["accessToken"])
		if len(accessToken) < 8 || len(accessToken) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid TickTick access token")
			return
		}
		projectID := strings.TrimSpace(body["projectId"])
		if !validTickTickProject(projectID) {
			writeError(w, http.StatusBadRequest, "ADDON_PROJECT", "Enter a valid TickTick project ID")
			return
		}
		credentials["accessToken"] = accessToken
		config["projectId"] = projectID
		accountLabel = "TickTick account"
	case "calendly":
		accessToken := strings.TrimSpace(body["accessToken"])
		if len(accessToken) < 8 || len(accessToken) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Calendly access token")
			return
		}
		eventTypeURI := strings.TrimSpace(body["eventTypeUri"])
		ownerURI := strings.TrimSpace(body["ownerUri"])
		if !validCalendlyURI(eventTypeURI, "/event_types/") || !validCalendlyURI(ownerURI, "/users/") {
			writeError(w, http.StatusBadRequest, "ADDON_URI", "Enter the Calendly event type and owner URIs")
			return
		}
		credentials["accessToken"] = accessToken
		config["eventTypeUri"] = eventTypeURI
		config["ownerUri"] = ownerURI
		accountLabel = "Calendly scheduling"
	case "jira":
		site := strings.ToLower(strings.TrimSpace(body["site"]))
		site = strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
		site = strings.TrimSuffix(site, "/")
		if !validJiraSite(site) {
			writeError(w, http.StatusBadRequest, "ADDON_SITE", "Enter a Jira Cloud site such as acme.atlassian.net")
			return
		}
		email := strings.TrimSpace(body["email"])
		if !looksLikeEmail(email) {
			writeError(w, http.StatusBadRequest, "ADDON_EMAIL", "Enter the Atlassian account email")
			return
		}
		apiToken := strings.TrimSpace(body["apiToken"])
		if len(apiToken) < 8 || len(apiToken) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Atlassian API token")
			return
		}
		projectKey := strings.ToUpper(strings.TrimSpace(body["projectKey"]))
		if !validJiraProjectKey(projectKey) {
			writeError(w, http.StatusBadRequest, "ADDON_PROJECT", "Enter a valid Jira project key")
			return
		}
		issueType := strings.TrimSpace(body["issueType"])
		if issueType == "" {
			issueType = "Task"
		}
		credentials["email"] = email
		credentials["apiToken"] = apiToken
		config["site"] = site
		config["projectKey"] = projectKey
		config["issueType"] = issueType
		accountLabel = site
	case "linear":
		apiKey := strings.TrimSpace(body["apiKey"])
		if len(apiKey) < 8 || len(apiKey) > 2000 {
			writeError(w, http.StatusBadRequest, "ADDON_CREDENTIALS", "Paste a valid Linear API key")
			return
		}
		teamID := strings.TrimSpace(body["teamId"])
		if !validLinearTeam(teamID) {
			writeError(w, http.StatusBadRequest, "ADDON_TEAM", "Enter a valid Linear team ID")
			return
		}
		credentials["apiKey"] = apiKey
		config["teamId"] = teamID
		accountLabel = "Linear workspace"
	default:
		writeError(w, http.StatusBadRequest, "ADDON_SETUP", "This add-on does not support direct setup yet")
		return
	}
	rawCredentials, _ := json.Marshal(credentials)
	sealed, err := s.sealWorkspaceToken(string(rawCredentials))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_SECRET", "Could not protect the add-on credentials")
		return
	}
	configJSON, _ := json.Marshal(config)
	connectionID := newID("acon")
	if definition.ConnectionScope == "tenant" {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE addon_connections SET status='revoked', revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND connection_scope='tenant' AND status='active'
		`, sess.User.TenantID, definition.ID)
		_, err = s.store.DB.Exec(r.Context(), `
			INSERT INTO addon_connections (
				id, tenant_id, addon_id, connection_scope, account_label,
				credentials_sealed, config, status, created_by
			) VALUES ($1,$2,$3,'tenant',$4,$5,$6,'active',$7)
		`, connectionID, sess.User.TenantID, definition.ID, accountLabel, sealed, configJSON, sess.User.ID)
		if err == nil {
			_, _ = s.store.DB.Exec(r.Context(), `
				INSERT INTO tenant_addons (id, tenant_id, addon_id, enabled, config, created_by, updated_by)
				VALUES ($1,$2,$3,true,$4,$5,$5)
				ON CONFLICT (tenant_id, addon_id) DO UPDATE SET enabled=true, config=EXCLUDED.config, updated_at=now(), updated_by=EXCLUDED.updated_by
			`, newID("tadd"), sess.User.TenantID, definition.ID, configJSON, sess.User.ID)
			_, _ = s.store.DB.Exec(r.Context(), `
				UPDATE addon_installs SET connection_id=$3, updated_at=now()
				WHERE tenant_id=$1 AND addon_id=$2
			`, sess.User.TenantID, definition.ID, connectionID)
			_, _ = s.store.DB.Exec(r.Context(), `
				UPDATE addon_requests SET status='approved', resolved_by=$3, resolved_at=now(), updated_at=now()
				WHERE tenant_id=$1 AND addon_id=$2 AND status='pending'
			`, sess.User.TenantID, definition.ID, sess.User.ID)
		}
	} else {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE addon_connections SET status='revoked', revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND connection_scope='user' AND owner_user_id=$3 AND status='active'
		`, sess.User.TenantID, definition.ID, sess.User.ID)
		_, err = s.store.DB.Exec(r.Context(), `
			INSERT INTO addon_connections (
				id, tenant_id, addon_id, connection_scope, owner_user_id, account_label,
				credentials_sealed, config, status, created_by
			) VALUES ($1,$2,$3,'user',$4,$5,$6,$7,'active',$4)
		`, connectionID, sess.User.TenantID, definition.ID, sess.User.ID, accountLabel, sealed, configJSON)
	}
	if err != nil {
		s.logger.Error("addon connect", "addon", definition.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "ADDON_CONNECT", "Could not save the add-on connection")
		return
	}
	if definition.ConnectionScope == "user" {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE addon_installs SET connection_id=$4, updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND user_id=$3
		`, sess.User.TenantID, definition.ID, sess.User.ID, connectionID)
	}
	s.audit(r, sess.User, "addon.connected", definition.Name, "Connected "+definition.Name+" for "+string(definition.ConnectionScope)+" scope")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "addonId": definition.ID, "configured": true})
}

func (s *Server) installAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	tenant := s.addonTenantRecord(r.Context(), sess.User.TenantID, definition.ID)
	effectiveTenant := addonEffectiveTenantEnabled(definition.ConnectionScope, tenant.Exists, tenant.Enabled)
	if !s.addonSystemEnabled(r.Context(), definition.ID) || !effectiveTenant {
		writeError(w, http.StatusForbidden, "ADDON_DISABLED", "This add-on is not enabled for the workspace")
		return
	}
	if !webmailAdminRole(sess.User.Role) && tenant.InstallPolicy == "approval_required" {
		if installed, _ := s.addonInstallActive(r.Context(), sess.User.TenantID, definition.ID, sess.User.ID); !installed {
			writeError(w, http.StatusConflict, "ADDON_APPROVAL_REQUIRED", "An administrator must approve this add-on for your account")
			return
		}
	}
	scope := definition.ConnectionScope
	connection, connected := s.addonActiveConnection(r.Context(), sess.User.TenantID, definition.ID, sess.User.ID, scope)
	if !connected {
		if definition.ConnectionScope == "tenant" {
			writeError(w, http.StatusConflict, "ADDON_SETUP_REQUIRED", "An administrator must set up this add-on first")
		} else {
			writeError(w, http.StatusConflict, "ADDON_CONNECTION_REQUIRED", "Connect your account before installing this add-on")
		}
		return
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO addon_installs (id, tenant_id, addon_id, user_id, connection_id, status)
		VALUES ($1,$2,$3,$4,$5,'active')
		ON CONFLICT (tenant_id, addon_id, user_id) DO UPDATE
		SET status='active', connection_id=EXCLUDED.connection_id, updated_at=now()
	`, newID("ain"), sess.User.TenantID, definition.ID, sess.User.ID, connection.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_INSTALL", "Could not install the add-on")
		return
	}
	s.audit(r, sess.User, "addon.installed", definition.Name, "Installed "+definition.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "installed": true})
}

func (s *Server) uninstallAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	_, _ = s.store.DB.Exec(r.Context(), `
		DELETE FROM addon_installs WHERE tenant_id=$1 AND addon_id=$2 AND user_id=$3
	`, sess.User.TenantID, definition.ID, sess.User.ID)
	if definition.ConnectionScope == "user" {
		_, _ = s.store.DB.Exec(r.Context(), `
			UPDATE addon_connections SET status='revoked', revoked_at=now(), updated_at=now()
			WHERE tenant_id=$1 AND addon_id=$2 AND connection_scope='user' AND owner_user_id=$3 AND status='active'
		`, sess.User.TenantID, definition.ID, sess.User.ID)
	}
	s.audit(r, sess.User, "addon.uninstalled", definition.Name, "Removed "+definition.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "installed": false})
}

func (s *Server) setTenantAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "ADDON_ADMIN", "Only an administrator can change workspace add-on policy")
		return
	}
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	var body struct {
		Enabled       *bool   `json:"enabled"`
		InstallPolicy *string `json:"installPolicy"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if body.Enabled == nil && body.InstallPolicy == nil {
		writeError(w, http.StatusBadRequest, "ADDON_POLICY", "Choose a workspace policy change")
		return
	}
	current := s.addonTenantRecord(r.Context(), sess.User.TenantID, definition.ID)
	enabled := current.Enabled
	if !current.Exists {
		enabled = true
	}
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	policy := current.InstallPolicy
	if policy == "" {
		policy = "self_service"
	}
	if body.InstallPolicy != nil {
		policy = strings.TrimSpace(*body.InstallPolicy)
		if policy != "self_service" && policy != "approval_required" {
			writeError(w, http.StatusBadRequest, "ADDON_POLICY", "Choose self-service or approval-required installation")
			return
		}
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO tenant_addons (id, tenant_id, addon_id, enabled, install_policy, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$6)
		ON CONFLICT (tenant_id, addon_id) DO UPDATE
		SET enabled=EXCLUDED.enabled, install_policy=EXCLUDED.install_policy,
		    updated_at=now(), updated_by=EXCLUDED.updated_by
	`, newID("tadd"), sess.User.TenantID, definition.ID, enabled, policy, sess.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_POLICY", "Could not update the add-on policy")
		return
	}
	detail := definition.Name + " workspace policy updated"
	if body.Enabled != nil {
		detail = "Disabled " + definition.Name + " for the workspace"
		if enabled {
			detail = "Enabled " + definition.Name + " for the workspace"
		}
	}
	if body.InstallPolicy != nil {
		detail += " · install policy " + policy
	}
	s.audit(r, sess.User, "addon.policy", definition.Name, detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled, "installPolicy": policy})
}

func (s *Server) requestAddon(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusBadRequest, "ADDON_ADMIN", "Administrators can set up this add-on directly")
		return
	}
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	view := s.addonView(r.Context(), sess, definition, false)
	if !stateBool(view, "systemEnabled") {
		writeError(w, http.StatusForbidden, "ADDON_DISABLED", "This add-on is not enabled on this system")
		return
	}
	status := stateString(view, "status")
	if status != "needs_setup" && status != "approval_required" && status != "blocked" && status != "requested" {
		writeError(w, http.StatusConflict, "ADDON_READY", "This add-on is already available to you")
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	note := strings.TrimSpace(body.Note)
	if len(note) > 500 {
		note = note[:500]
	}
	requestType := "setup"
	if status == "approval_required" {
		requestType = "install"
	}
	_, err := s.store.DB.Exec(r.Context(), `
		INSERT INTO addon_requests (id, tenant_id, addon_id, user_id, request_type, note, status)
		VALUES ($1,$2,$3,$4,$5,$6,'pending')
		ON CONFLICT DO NOTHING
	`, newID("arq"), sess.User.TenantID, definition.ID, sess.User.ID, requestType, note)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ADDON_REQUEST", "Could not send the request to your administrator")
		return
	}
	s.audit(r, sess.User, "addon.requested", definition.Name, "Requested workspace setup for "+definition.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": true})
}

func (s *Server) resolveAddonRequest(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	if !webmailAdminRole(sess.User.Role) {
		writeError(w, http.StatusForbidden, "ADDON_ADMIN", "Only an administrator can resolve add-on requests")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != "approved" && status != "denied" {
		writeError(w, http.StatusBadRequest, "ADDON_REQUEST", "Choose approved or denied")
		return
	}
	var addonID, requestUserID, requestType string
	err := s.store.DB.QueryRow(r.Context(), `
		UPDATE addon_requests
		SET status=$3, resolved_by=$4, resolved_at=now(), updated_at=now()
		WHERE id=$1 AND tenant_id=$2 AND status='pending'
		RETURNING addon_id, user_id::text, request_type
	`, r.PathValue("id"), sess.User.TenantID, status, sess.User.ID).Scan(&addonID, &requestUserID, &requestType)
	if err != nil {
		writeError(w, http.StatusNotFound, "ADDON_REQUEST", "Request not found")
		return
	}
	if status == "approved" && requestType == "install" {
		if definition, ok := addonDefinitionByID(addonID); ok {
			if connection, connected := s.addonActiveConnection(r.Context(), sess.User.TenantID, addonID, requestUserID, definition.ConnectionScope); connected {
				_, _ = s.store.DB.Exec(r.Context(), `
					INSERT INTO addon_installs (id, tenant_id, addon_id, user_id, connection_id, status)
					VALUES ($1,$2,$3,$4,$5,'active')
					ON CONFLICT (tenant_id, addon_id, user_id) DO UPDATE
					SET status='active', connection_id=EXCLUDED.connection_id, updated_at=now()
				`, newID("ain"), sess.User.TenantID, addonID, requestUserID, connection.ID)
			}
		}
	}
	s.audit(r, sess.User, "addon.request_resolved", addonID, "Add-on request "+status+" · "+requestType)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status, "requestType": requestType})
}

type addonMailMessage struct {
	Subject   string
	FromName  string
	FromEmail string
	BodyText  string
	Snippet   string
}

func (s *Server) loadAddonMessage(ctx context.Context, userID, messageID string) (addonMailMessage, bool) {
	var message addonMailMessage
	err := s.store.DB.QueryRow(ctx, `
		SELECT subject, from_name, from_email::text, body_text, snippet
		FROM mail_messages WHERE id=$1 AND user_id=$2
	`, messageID, userID).Scan(&message.Subject, &message.FromName, &message.FromEmail, &message.BodyText, &message.Snippet)
	return message, err == nil
}

func (s *Server) runAddonAction(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	definition, ok := addonDefinitionByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "ADDON_NOT_FOUND", "Add-on not found")
		return
	}
	if installed, _ := s.addonInstallActive(r.Context(), sess.User.TenantID, definition.ID, sess.User.ID); !installed {
		writeError(w, http.StatusForbidden, "ADDON_NOT_INSTALLED", "Install this add-on before using it")
		return
	}
	tenant := s.addonTenantRecord(r.Context(), sess.User.TenantID, definition.ID)
	if !s.addonSystemEnabled(r.Context(), definition.ID) || !addonEffectiveTenantEnabled(definition.ConnectionScope, tenant.Exists, tenant.Enabled) {
		writeError(w, http.StatusForbidden, "ADDON_DISABLED", "This add-on is not enabled for the workspace")
		return
	}
	connection, connected := s.addonActiveConnection(r.Context(), sess.User.TenantID, definition.ID, sess.User.ID, definition.ConnectionScope)
	if !connected {
		writeError(w, http.StatusConflict, "ADDON_SETUP_REQUIRED", "Reconnect this add-on before using it")
		return
	}
	var credentials map[string]string
	plain, err := s.openWorkspaceToken(connection.Sealed)
	if err != nil || json.Unmarshal([]byte(plain), &credentials) != nil {
		writeError(w, http.StatusConflict, "ADDON_SETUP_REQUIRED", "Reconnect this add-on before using it")
		return
	}
	var body struct {
		MessageID string `json:"messageId"`
	}
	if err := decodeJSON(w, r, &body, 16*1024); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	message, found := s.loadAddonMessage(r.Context(), sess.User.ID, strings.TrimSpace(body.MessageID))
	if !found {
		writeError(w, http.StatusNotFound, "MESSAGE_NOT_FOUND", "Message not found")
		return
	}
	action := r.PathValue("action")
	var result map[string]any
	switch definition.ID {
	case "deepl":
		if action != "translate" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		target := stateString(connection.Config, "targetLanguage")
		if target == "" {
			target = "EN-US"
		}
		translation, err := deeplTranslate(r.Context(), credentials["apiKey"], message, target)
		if err != nil {
			s.logger.Warn("deepl translate", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "DeepL could not translate this message")
			return
		}
		result = map[string]any{"translation": translation}
	case "todoist":
		if action != "create-task" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		taskID, err := todoistCreateTask(r.Context(), credentials["apiKey"], message)
		if err != nil {
			s.logger.Warn("todoist task", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Todoist could not create this task")
			return
		}
		result = map[string]any{"taskId": taskID}
	case "slack":
		if action != "send-to-channel" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		if err := slackPost(r.Context(), credentials["webhookUrl"], message); err != nil {
			s.logger.Warn("slack post", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Slack could not accept this message")
			return
		}
		result = map[string]any{"posted": true}
	case "hubspot":
		if action != "log-email" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		note, err := hubspotLogEmail(r.Context(), credentials["accessToken"], message)
		if err != nil {
			s.logger.Warn("hubspot log email", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", err.Error())
			return
		}
		result = note
	case "asana":
		if action != "create-task" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		task, err := asanaCreateTask(r.Context(), credentials["apiKey"], stateString(connection.Config, "projectGid"), message)
		if err != nil {
			s.logger.Warn("asana task", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Asana could not create this task")
			return
		}
		result = task
	case "pagerduty":
		if action != "trigger-incident" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		severity := stateString(connection.Config, "severity")
		if severity == "" {
			severity = "error"
		}
		incident, err := pagerDutyTrigger(r.Context(), credentials["routingKey"], severity, message)
		if err != nil {
			s.logger.Warn("pagerduty trigger", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "PagerDuty could not create this incident")
			return
		}
		result = incident
	case "zoom":
		if action != "create-meeting" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		duration := 30
		if parsed, parseErr := strconv.Atoi(stateString(connection.Config, "duration")); parseErr == nil && parsed > 0 {
			duration = parsed
		}
		meeting, err := zoomCreateMeeting(r.Context(), credentials["accountId"], credentials["clientId"], credentials["clientSecret"], duration, message)
		if err != nil {
			s.logger.Warn("zoom meeting", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Zoom could not create this meeting")
			return
		}
		result = meeting
	case "pipedrive":
		if action != "log-email" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		note, err := pipedriveLogEmail(r.Context(), credentials["apiToken"], message)
		if err != nil {
			s.logger.Warn("pipedrive note", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Pipedrive could not log this email")
			return
		}
		result = note
	case "zoho":
		if action != "log-email" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		note, err := zohoCRMLogEmail(r.Context(), credentials["clientId"], credentials["clientSecret"], credentials["refreshToken"], stateString(connection.Config, "region"), message)
		if err != nil {
			s.logger.Warn("zoho note", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Zoho CRM could not log this email")
			return
		}
		result = note
	case "clickup":
		if action != "create-task" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		task, err := clickUpCreateTask(r.Context(), credentials["apiToken"], stateString(connection.Config, "listId"), message)
		if err != nil {
			s.logger.Warn("clickup task", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "ClickUp could not create this task")
			return
		}
		result = task
	case "zendesk":
		if action != "create-ticket" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		ticket, err := zendeskCreateTicket(r.Context(), stateString(connection.Config, "subdomain"), credentials["email"], credentials["apiToken"], message)
		if err != nil {
			s.logger.Warn("zendesk ticket", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Zendesk could not create this ticket")
			return
		}
		result = ticket
	case "salesforce":
		if action != "log-email" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		task, err := salesforceLogEmail(r.Context(), stateString(connection.Config, "loginUrl"), credentials["clientId"], credentials["clientSecret"], credentials["refreshToken"], message)
		if err != nil {
			s.logger.Warn("salesforce task", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Salesforce could not log this email")
			return
		}
		result = task
	case "freshdesk":
		if action != "create-ticket" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		ticket, err := freshdeskCreateTicket(r.Context(), stateString(connection.Config, "domain"), credentials["apiKey"], message)
		if err != nil {
			s.logger.Warn("freshdesk ticket", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Freshdesk could not create this ticket")
			return
		}
		result = ticket
	case "intercom":
		if action != "add-note" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		note, err := intercomAddNote(r.Context(), stateString(connection.Config, "region"), credentials["accessToken"], message)
		if err != nil {
			s.logger.Warn("intercom note", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Intercom could not add this note")
			return
		}
		result = note
	case "ticktick":
		if action != "create-task" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		task, err := tickTickCreateTask(r.Context(), credentials["accessToken"], stateString(connection.Config, "projectId"), message)
		if err != nil {
			s.logger.Warn("ticktick task", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "TickTick could not create this task")
			return
		}
		result = task
	case "calendly":
		if action != "create-link" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		link, err := calendlyCreateLink(r.Context(), credentials["accessToken"], stateString(connection.Config, "eventTypeUri"), stateString(connection.Config, "ownerUri"))
		if err != nil {
			s.logger.Warn("calendly link", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Calendly could not create this booking link")
			return
		}
		result = link
	case "jira":
		if action != "create-issue" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		issue, err := jiraCreateIssue(r.Context(), stateString(connection.Config, "site"), credentials["email"], credentials["apiToken"], stateString(connection.Config, "projectKey"), stateString(connection.Config, "issueType"), message)
		if err != nil {
			s.logger.Warn("jira issue", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Jira could not create this issue")
			return
		}
		result = issue
	case "linear":
		if action != "create-issue" {
			writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
			return
		}
		issue, err := linearCreateIssue(r.Context(), credentials["apiKey"], stateString(connection.Config, "teamId"), message)
		if err != nil {
			s.logger.Warn("linear issue", "err", err)
			writeError(w, http.StatusBadGateway, "ADDON_PROVIDER", "Linear could not create this issue")
			return
		}
		result = issue
	default:
		writeError(w, http.StatusNotFound, "ADDON_ACTION", "Add-on action not found")
		return
	}
	s.audit(r, sess.User, "addon.action", definition.Name, "Used "+definition.Name+" action: "+action)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

var (
	addonProviderMu    sync.Mutex
	addonProviderGates = map[string]chan struct{}{}
	addonProviderLast  = map[string]time.Time{}
)

func addonProviderGate(provider string) chan struct{} {
	addonProviderMu.Lock()
	defer addonProviderMu.Unlock()
	gate := addonProviderGates[provider]
	if gate == nil {
		gate = make(chan struct{}, 3)
		addonProviderGates[provider] = gate
	}
	return gate
}

// addonDo serializes provider calls a little, spaces requests per provider and
// retries transient 429/5xx responses. The gate is deliberately in-memory: it
// protects a single Alt8Mail instance from provider bursts without adding a
// database dependency to every action.
func addonDo(ctx context.Context, provider string, request *http.Request) (*http.Response, error) {
	gate := addonProviderGate(provider)
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	addonProviderMu.Lock()
	last := addonProviderLast[provider]
	if wait := 180*time.Millisecond - time.Since(last); wait > 0 {
		addonProviderMu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		addonProviderMu.Lock()
	}
	addonProviderLast[provider] = time.Now()
	addonProviderMu.Unlock()

	for attempt := 0; attempt < 3; attempt++ {
		current := request
		if attempt > 0 {
			current = request.Clone(ctx)
			if request.Body != nil {
				if request.GetBody == nil {
					return nil, fmt.Errorf("provider retry requires a replayable request")
				}
				body, err := request.GetBody()
				if err != nil {
					return nil, err
				}
				current.Body = body
			}
		}
		response, err := addonHTTPClient.Do(current)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusTooManyRequests && response.StatusCode < 500 {
			return response, nil
		}
		delay := time.Duration(attempt+1) * 500 * time.Millisecond
		if retryAfter := strings.TrimSpace(response.Header.Get("Retry-After")); retryAfter != "" {
			if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
				delay = time.Duration(seconds) * time.Second
			}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if attempt == 2 {
			return nil, fmt.Errorf("%s returned %d", provider, response.StatusCode)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%s request failed", provider)
}

func hubspotLogEmail(ctx context.Context, accessToken string, message addonMailMessage) (map[string]any, error) {
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" {
		return nil, fmt.Errorf("HubSpot needs the sender's email address")
	}
	contactURL := "https://api.hubapi.com/crm/v3/objects/contacts/" + url.PathEscape(fromEmail) + "?idProperty=email&properties=email,firstname,lastname,company"
	contactRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, contactURL, nil)
	if err != nil {
		return nil, err
	}
	contactRequest.Header.Set("Authorization", "Bearer "+accessToken)
	contactResponse, err := addonDo(ctx, "hubspot", contactRequest)
	if err != nil {
		return nil, err
	}
	contactRaw, _ := io.ReadAll(io.LimitReader(contactResponse.Body, 256*1024))
	contactResponse.Body.Close()
	if contactResponse.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no HubSpot contact found for %s", fromEmail)
	}
	if contactResponse.StatusCode < 200 || contactResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("HubSpot contact lookup returned %d", contactResponse.StatusCode)
	}
	var contact struct {
		ID         string            `json:"id"`
		Properties map[string]string `json:"properties"`
	}
	if err := json.Unmarshal(contactRaw, &contact); err != nil || contact.ID == "" {
		return nil, fmt.Errorf("HubSpot returned an invalid contact")
	}
	noteBody := strings.TrimSpace("Email from " + firstNonEmpty(message.FromName, message.FromEmail) + "\n" +
		"Subject: " + firstNonEmpty(message.Subject, "(no subject)") + "\n\n" +
		firstNonEmpty(message.Snippet, message.BodyText))
	if len(noteBody) > 20000 {
		noteBody = noteBody[:20000]
	}
	notePayload, _ := json.Marshal(map[string]any{
		"properties": map[string]string{
			"hs_timestamp": time.Now().UTC().Format(time.RFC3339),
			"hs_note_body": noteBody,
		},
	})
	noteRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.hubapi.com/crm/v3/objects/notes", strings.NewReader(string(notePayload)))
	if err != nil {
		return nil, err
	}
	noteRequest.Header.Set("Authorization", "Bearer "+accessToken)
	noteRequest.Header.Set("Content-Type", "application/json")
	noteResponse, err := addonDo(ctx, "hubspot", noteRequest)
	if err != nil {
		return nil, err
	}
	noteRaw, _ := io.ReadAll(io.LimitReader(noteResponse.Body, 256*1024))
	noteResponse.Body.Close()
	if noteResponse.StatusCode < 200 || noteResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("HubSpot note creation returned %d", noteResponse.StatusCode)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(noteRaw, &note); err != nil || note.ID == "" {
		return nil, fmt.Errorf("HubSpot returned an invalid note")
	}
	associateURL := "https://api.hubapi.com/crm/v4/objects/notes/" + url.PathEscape(note.ID) +
		"/associations/default/contacts/" + url.PathEscape(contact.ID)
	associateRequest, err := http.NewRequestWithContext(ctx, http.MethodPut, associateURL, strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	associateRequest.Header.Set("Authorization", "Bearer "+accessToken)
	associateRequest.Header.Set("Content-Type", "application/json")
	associateResponse, err := addonDo(ctx, "hubspot", associateRequest)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(associateResponse.Body, 32*1024))
	associateResponse.Body.Close()
	if associateResponse.StatusCode < 200 || associateResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("HubSpot note was created but could not be attached to the contact")
	}
	contactName := strings.TrimSpace(contact.Properties["firstname"] + " " + contact.Properties["lastname"])
	if contactName == "" {
		contactName = firstNonEmpty(contact.Properties["email"], fromEmail)
	}
	return map[string]any{"contactId": contact.ID, "contactName": contactName, "noteId": note.ID}, nil
}

func asanaCreateTask(ctx context.Context, accessToken, projectGID string, message addonMailMessage) (map[string]any, error) {
	if !validAsanaGID(projectGID) {
		return nil, fmt.Errorf("reconnect Asana with a valid project GID")
	}
	name := strings.TrimSpace(message.Subject)
	if name == "" {
		name = "Email follow-up"
	}
	if len(name) > 500 {
		name = name[:500]
	}
	notes := strings.TrimSpace("From: " + firstNonEmpty(message.FromName, message.FromEmail) + "\n\n" +
		firstNonEmpty(message.Snippet, message.BodyText))
	if len(notes) > 15000 {
		notes = notes[:15000]
	}
	payload, _ := json.Marshal(map[string]any{
		"data": map[string]any{"name": name, "notes": notes, "projects": []string{projectGID}},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://app.asana.com/api/1.0/tasks", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "asana", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Asana returned %d", response.StatusCode)
	}
	var result struct {
		Data struct {
			GID          string `json:"gid"`
			PermalinkURL string `json:"permalink_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Data.GID == "" {
		return nil, fmt.Errorf("Asana returned an invalid task")
	}
	return map[string]any{"taskId": result.Data.GID, "taskUrl": result.Data.PermalinkURL}, nil
}

func pagerDutyTrigger(ctx context.Context, routingKey, severity string, message addonMailMessage) (map[string]any, error) {
	if !validPagerDutySeverity(severity) {
		severity = "error"
	}
	summary := strings.TrimSpace(message.Subject)
	if summary == "" {
		summary = "Email from " + firstNonEmpty(message.FromName, message.FromEmail)
	}
	if len(summary) > 1024 {
		summary = summary[:1024]
	}
	payload, _ := json.Marshal(map[string]any{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary": summary, "source": "alt8mail", "severity": severity,
			"custom_details": map[string]any{
				"from":  firstNonEmpty(message.FromName, message.FromEmail),
				"email": message.FromEmail, "subject": message.Subject,
				"preview": firstNonEmpty(message.Snippet, message.BodyText),
			},
		},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://events.pagerduty.com/v2/enqueue", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "pagerduty", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 128*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("PagerDuty returned %d", response.StatusCode)
	}
	var result struct {
		Status   string `json:"status"`
		DedupKey string `json:"dedup_key"`
	}
	_ = json.Unmarshal(raw, &result)
	return map[string]any{"status": result.Status, "dedupKey": result.DedupKey}, nil
}

func zoomCreateMeeting(ctx context.Context, accountID, clientID, clientSecret string, duration int, message addonMailMessage) (map[string]any, error) {
	tokenURL := "https://zoom.us/oauth/token?grant_type=account_credentials&account_id=" + url.QueryEscape(accountID)
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, nil)
	if err != nil {
		return nil, err
	}
	tokenRequest.SetBasicAuth(clientID, clientSecret)
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := addonDo(ctx, "zoom", tokenRequest)
	if err != nil {
		return nil, err
	}
	tokenRaw, _ := io.ReadAll(io.LimitReader(tokenResponse.Body, 128*1024))
	tokenResponse.Body.Close()
	if tokenResponse.StatusCode < 200 || tokenResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Zoom token exchange returned %d", tokenResponse.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenRaw, &token); err != nil || token.AccessToken == "" {
		return nil, fmt.Errorf("Zoom returned an invalid access token")
	}
	topic := strings.TrimSpace(message.Subject)
	if topic == "" {
		topic = "Email meeting"
	}
	if len(topic) > 200 {
		topic = topic[:200]
	}
	payload, _ := json.Marshal(map[string]any{
		"topic": topic, "type": 2,
		"start_time": time.Now().UTC().Add(5 * time.Minute).Format("2006-01-02T15:04:05Z"),
		"duration":   duration, "timezone": "UTC",
		"settings": map[string]any{"waiting_room": true, "join_before_host": false},
	})
	meetingRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.zoom.us/v2/users/me/meetings", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	meetingRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	meetingRequest.Header.Set("Content-Type", "application/json")
	meetingResponse, err := addonDo(ctx, "zoom", meetingRequest)
	if err != nil {
		return nil, err
	}
	meetingRaw, _ := io.ReadAll(io.LimitReader(meetingResponse.Body, 256*1024))
	meetingResponse.Body.Close()
	if meetingResponse.StatusCode < 200 || meetingResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Zoom meeting creation returned %d", meetingResponse.StatusCode)
	}
	var meeting struct {
		ID        any    `json:"id"`
		JoinURL   string `json:"join_url"`
		StartTime string `json:"start_time"`
	}
	if err := json.Unmarshal(meetingRaw, &meeting); err != nil || meeting.JoinURL == "" {
		return nil, fmt.Errorf("Zoom returned an invalid meeting")
	}
	return map[string]any{"meetingId": fmt.Sprint(meeting.ID), "meetingUrl": meeting.JoinURL, "startTime": meeting.StartTime, "duration": duration}, nil
}

func validateSlackWebhook(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strings.HasPrefix(parsed.Path, "/services/") {
		return fmt.Errorf("Paste the full HTTPS Slack incoming webhook URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "hooks.slack.com" && host != "hooks.slack-gov.com" {
		return fmt.Errorf("Use a webhook URL from hooks.slack.com")
	}
	return nil
}

func validAsanaGID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

func validPagerDutySeverity(value string) bool {
	switch value {
	case "critical", "error", "warning", "info":
		return true
	}
	return false
}

func validZohoRegion(value string) bool {
	switch value {
	case "com", "eu", "in", "com.au", "jp":
		return true
	}
	return false
}

func zohoEndpoints(region string) (accountsHost, apiHost string, ok bool) {
	if !validZohoRegion(region) {
		return "", "", false
	}
	return "accounts.zoho." + region, "www.zohoapis." + region, true
}

func validClickUpList(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validZendeskSubdomain(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			continue
		}
		if char == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func validSalesforceLogin(value string) bool {
	return value == "https://login.salesforce.com" || value == "https://test.salesforce.com"
}

func validIntercomRegion(value string) bool {
	switch value {
	case "us", "eu", "au":
		return true
	}
	return false
}

func validTickTickProject(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func validCalendlyURI(value, pathFragment string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "api.calendly.com" {
		return false
	}
	if !strings.Contains(parsed.Path, pathFragment) {
		return false
	}
	tail := strings.TrimPrefix(parsed.Path[strings.Index(parsed.Path, pathFragment)+len(pathFragment):], "/")
	return tail != "" && !strings.Contains(tail, "/")
}

func validJiraSite(value string) bool {
	suffix := ".atlassian.net"
	if !strings.HasSuffix(value, suffix) {
		return false
	}
	label := strings.TrimSuffix(value, suffix)
	if len(label) < 1 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, char := range label {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validJiraProjectKey(value string) bool {
	if len(value) < 2 || len(value) > 10 {
		return false
	}
	for index, char := range value {
		if char >= 'A' && char <= 'Z' {
			continue
		}
		if index > 0 && (char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func validLinearTeam(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func allowedDeepLTarget(target string) bool {
	switch target {
	case "EN-US", "EN-GB", "ES", "FR", "DE", "IT", "PT-BR", "PT-PT", "NL", "JA", "KO", "ZH", "AR", "RU", "PL", "SV", "TR":
		return true
	}
	return false
}

func deeplTranslate(ctx context.Context, apiKey string, message addonMailMessage, target string) (map[string]any, error) {
	endpoint := "https://api.deepl.com/v2/translate"
	if strings.HasSuffix(apiKey, ":fx") {
		endpoint = "https://api-free.deepl.com/v2/translate"
	}
	text := strings.TrimSpace(message.Subject + "\n\n" + firstNonEmpty(message.BodyText, message.Snippet))
	if len(text) > 40000 {
		text = text[:40000]
	}
	payload, _ := json.Marshal(map[string]any{"text": []string{text}, "target_lang": target})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "DeepL-Auth-Key "+apiKey)
	response, err := addonDo(ctx, "deepl", request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("DeepL returned %d", response.StatusCode)
	}
	var result struct {
		Translations []struct {
			Text                   string `json:"text"`
			DetectedSourceLanguage string `json:"detected_source_language"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Translations) == 0 {
		return nil, fmt.Errorf("DeepL returned an empty translation")
	}
	return map[string]any{
		"text": result.Translations[0].Text, "sourceLanguage": result.Translations[0].DetectedSourceLanguage,
		"targetLanguage": target,
	}, nil
}

func todoistCreateTask(ctx context.Context, apiKey string, message addonMailMessage) (string, error) {
	description := strings.TrimSpace("From: " + firstNonEmpty(message.FromName, message.FromEmail) + "\n\n" + firstNonEmpty(message.Snippet, message.BodyText))
	if len(description) > 15000 {
		description = description[:15000]
	}
	content := strings.TrimSpace(message.Subject)
	if content == "" {
		content = "Email follow-up"
	}
	if len(content) > 500 {
		content = content[:500]
	}
	payload, _ := json.Marshal(map[string]string{"content": content, "description": description})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.todoist.com/api/v1/tasks", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := addonDo(ctx, "todoist", request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 256*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Todoist returned %d", response.StatusCode)
	}
	var result struct {
		ID any `json:"id"`
	}
	_ = json.Unmarshal(raw, &result)
	return fmt.Sprint(result.ID), nil
}

func addonProviderNote(message addonMailMessage) string {
	note := strings.TrimSpace("Email from " + firstNonEmpty(message.FromName, message.FromEmail) + "\n" +
		"Subject: " + firstNonEmpty(message.Subject, "(no subject)") + "\n\n" +
		firstNonEmpty(message.Snippet, message.BodyText))
	if len(note) > 20000 {
		note = note[:20000]
	}
	return note
}

func pipedriveLogEmail(ctx context.Context, apiToken string, message addonMailMessage) (map[string]any, error) {
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" {
		return nil, fmt.Errorf("Pipedrive needs the sender's email address")
	}
	searchURL := "https://api.pipedrive.com/v1/persons/search?term=" + url.QueryEscape(fromEmail) + "&fields=email&exact_match=true&limit=1"
	searchRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	searchRequest.Header.Set("x-api-token", apiToken)
	searchResponse, err := addonDo(ctx, "pipedrive", searchRequest)
	if err != nil {
		return nil, err
	}
	searchRaw, _ := io.ReadAll(io.LimitReader(searchResponse.Body, 256*1024))
	searchResponse.Body.Close()
	if searchResponse.StatusCode < 200 || searchResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Pipedrive person search returned %d", searchResponse.StatusCode)
	}
	var search struct {
		Success bool `json:"success"`
		Data    struct {
			Items []struct {
				Item struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"item"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(searchRaw, &search); err != nil || !search.Success {
		return nil, fmt.Errorf("Pipedrive returned an invalid person search")
	}
	if len(search.Data.Items) == 0 || search.Data.Items[0].Item.ID == 0 {
		return nil, fmt.Errorf("no Pipedrive person found for %s", fromEmail)
	}
	person := search.Data.Items[0].Item
	payload, _ := json.Marshal(map[string]any{
		"content":   addonProviderNote(message),
		"person_id": person.ID,
	})
	noteRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pipedrive.com/v1/notes", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	noteRequest.Header.Set("x-api-token", apiToken)
	noteRequest.Header.Set("Content-Type", "application/json")
	noteResponse, err := addonDo(ctx, "pipedrive", noteRequest)
	if err != nil {
		return nil, err
	}
	noteRaw, _ := io.ReadAll(io.LimitReader(noteResponse.Body, 256*1024))
	noteResponse.Body.Close()
	if noteResponse.StatusCode < 200 || noteResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Pipedrive note create returned %d", noteResponse.StatusCode)
	}
	var note struct {
		Success bool `json:"success"`
		Data    struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(noteRaw, &note); err != nil || !note.Success {
		return nil, fmt.Errorf("Pipedrive returned an invalid note")
	}
	return map[string]any{
		"personId": person.ID, "noteId": note.Data.ID,
		"contactName": firstNonEmpty(person.Name, fromEmail),
	}, nil
}

func zohoCRMLogEmail(ctx context.Context, clientID, clientSecret, refreshToken, region string, message addonMailMessage) (map[string]any, error) {
	accountsHost, apiHost, ok := zohoEndpoints(region)
	if !ok {
		return nil, fmt.Errorf("unsupported Zoho data centre")
	}
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" || !looksLikeEmail(fromEmail) {
		return nil, fmt.Errorf("Zoho CRM needs the sender's email address")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+accountsHost+"/oauth/v2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := addonDo(ctx, "zoho", tokenRequest)
	if err != nil {
		return nil, err
	}
	tokenRaw, _ := io.ReadAll(io.LimitReader(tokenResponse.Body, 256*1024))
	tokenResponse.Body.Close()
	if tokenResponse.StatusCode < 200 || tokenResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Zoho token endpoint returned %d", tokenResponse.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(tokenRaw, &token); err != nil || token.AccessToken == "" {
		return nil, fmt.Errorf("Zoho did not return an access token: %s", firstNonEmpty(token.Error, "unknown error"))
	}
	searchURL := "https://" + apiHost + "/crm/v8/Contacts/search?email=" + url.QueryEscape(fromEmail)
	searchRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	searchRequest.Header.Set("Authorization", "Zoho-oauthtoken "+token.AccessToken)
	searchResponse, err := addonDo(ctx, "zoho", searchRequest)
	if err != nil {
		return nil, err
	}
	searchRaw, _ := io.ReadAll(io.LimitReader(searchResponse.Body, 512*1024))
	searchResponse.Body.Close()
	if searchResponse.StatusCode == http.StatusNoContent {
		return nil, fmt.Errorf("no Zoho CRM contact found for %s", fromEmail)
	}
	if searchResponse.StatusCode < 200 || searchResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Zoho contact search returned %d", searchResponse.StatusCode)
	}
	var search struct {
		Data []struct {
			ID       string `json:"id"`
			FullName string `json:"Full_Name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(searchRaw, &search); err != nil || len(search.Data) == 0 || search.Data[0].ID == "" {
		return nil, fmt.Errorf("no Zoho CRM contact found for %s", fromEmail)
	}
	contact := search.Data[0]
	payload, _ := json.Marshal(map[string]any{
		"data": []map[string]any{{
			"Note_Title":   firstNonEmpty(message.Subject, "Email from "+fromEmail),
			"Note_Content": addonProviderNote(message),
			"Parent_Id":    contact.ID,
			"se_module":    "Contacts",
		}},
		"trigger": []string{},
	})
	noteRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+apiHost+"/crm/v8/Notes", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	noteRequest.Header.Set("Authorization", "Zoho-oauthtoken "+token.AccessToken)
	noteRequest.Header.Set("Content-Type", "application/json")
	noteResponse, err := addonDo(ctx, "zoho", noteRequest)
	if err != nil {
		return nil, err
	}
	noteRaw, _ := io.ReadAll(io.LimitReader(noteResponse.Body, 512*1024))
	noteResponse.Body.Close()
	if noteResponse.StatusCode < 200 || noteResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Zoho note create returned %d", noteResponse.StatusCode)
	}
	var note struct {
		Data []struct {
			Code    string `json:"code"`
			Details struct {
				ID string `json:"id"`
			} `json:"details"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(noteRaw, &note); err != nil || len(note.Data) == 0 || note.Data[0].Code != "SUCCESS" {
		return nil, fmt.Errorf("Zoho rejected the note")
	}
	return map[string]any{
		"contactId": contact.ID, "noteId": note.Data[0].Details.ID,
		"contactName": firstNonEmpty(contact.FullName, fromEmail),
	}, nil
}

func clickUpCreateTask(ctx context.Context, apiToken, listID string, message addonMailMessage) (map[string]any, error) {
	if !validClickUpList(listID) {
		return nil, fmt.Errorf("ClickUp list ID is invalid")
	}
	payload, _ := json.Marshal(map[string]any{
		"name":                 firstNonEmpty(message.Subject, "Email task"),
		"markdown_description": addonProviderNote(message),
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.clickup.com/api/v2/list/"+url.PathEscape(listID)+"/task", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", apiToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "clickup", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("ClickUp task create returned %d", response.StatusCode)
	}
	var task struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		return nil, fmt.Errorf("ClickUp returned an invalid task")
	}
	return map[string]any{"taskId": task.ID, "taskUrl": task.URL, "listId": listID}, nil
}

func zendeskCreateTicket(ctx context.Context, subdomain, email, apiToken string, message addonMailMessage) (map[string]any, error) {
	if !validZendeskSubdomain(subdomain) {
		return nil, fmt.Errorf("Zendesk subdomain is invalid")
	}
	ticket := map[string]any{
		"subject": firstNonEmpty(message.Subject, "Email from "+firstNonEmpty(message.FromEmail, "customer")),
		"comment": map[string]string{"body": addonProviderNote(message)},
		"tags":    []string{"alt8mail"},
	}
	if sender := strings.TrimSpace(message.FromEmail); looksLikeEmail(sender) {
		ticket["requester"] = map[string]string{
			"email": sender,
			"name":  firstNonEmpty(message.FromName, sender),
		}
	}
	payload, _ := json.Marshal(map[string]any{"ticket": ticket})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+subdomain+".zendesk.com/api/v2/tickets.json", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(email+"/token", apiToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "zendesk", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Zendesk ticket create returned %d", response.StatusCode)
	}
	var result struct {
		Ticket struct {
			ID int64 `json:"id"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Ticket.ID == 0 {
		return nil, fmt.Errorf("Zendesk returned an invalid ticket")
	}
	return map[string]any{
		"ticketId":  result.Ticket.ID,
		"ticketUrl": "https://" + subdomain + ".zendesk.com/agent/tickets/" + strconv.FormatInt(result.Ticket.ID, 10),
	}, nil
}

func salesforceLogEmail(ctx context.Context, loginURL, clientID, clientSecret, refreshToken string, message addonMailMessage) (map[string]any, error) {
	if !validSalesforceLogin(loginURL) {
		return nil, fmt.Errorf("Salesforce login URL is invalid")
	}
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" || !looksLikeEmail(fromEmail) {
		return nil, fmt.Errorf("Salesforce needs the sender's email address")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL+"/services/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	tokenRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenResponse, err := addonDo(ctx, "salesforce", tokenRequest)
	if err != nil {
		return nil, err
	}
	tokenRaw, _ := io.ReadAll(io.LimitReader(tokenResponse.Body, 256*1024))
	tokenResponse.Body.Close()
	if tokenResponse.StatusCode < 200 || tokenResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Salesforce token endpoint returned %d", tokenResponse.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		InstanceURL string `json:"instance_url"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(tokenRaw, &token); err != nil || token.AccessToken == "" || token.InstanceURL == "" {
		return nil, fmt.Errorf("Salesforce did not return an access token: %s", firstNonEmpty(token.Error, "unknown error"))
	}
	instance, err := url.Parse(token.InstanceURL)
	if err != nil || instance.Scheme != "https" || !strings.HasSuffix(strings.ToLower(instance.Hostname()), ".salesforce.com") {
		return nil, fmt.Errorf("Salesforce returned an unexpected instance URL")
	}
	instanceURL := strings.TrimRight(token.InstanceURL, "/")
	soql := "SELECT Id, Name FROM Contact WHERE Email = '" + strings.ReplaceAll(fromEmail, "'", "\\'") + "' LIMIT 1"
	queryRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, instanceURL+"/services/data/v62.0/query?q="+url.QueryEscape(soql), nil)
	if err != nil {
		return nil, err
	}
	queryRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	queryResponse, err := addonDo(ctx, "salesforce", queryRequest)
	if err != nil {
		return nil, err
	}
	queryRaw, _ := io.ReadAll(io.LimitReader(queryResponse.Body, 512*1024))
	queryResponse.Body.Close()
	if queryResponse.StatusCode < 200 || queryResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Salesforce contact query returned %d", queryResponse.StatusCode)
	}
	var contacts struct {
		Records []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"records"`
	}
	if err := json.Unmarshal(queryRaw, &contacts); err != nil || len(contacts.Records) == 0 {
		return nil, fmt.Errorf("no Salesforce contact found for %s", fromEmail)
	}
	contact := contacts.Records[0]
	subject := firstNonEmpty(message.Subject, "Email from "+fromEmail)
	if len(subject) > 255 {
		subject = subject[:255]
	}
	taskPayload, _ := json.Marshal(map[string]any{
		"Subject":      subject,
		"Description":  addonProviderNote(message),
		"WhoId":        contact.ID,
		"Status":       "Completed",
		"ActivityDate": time.Now().UTC().Format("2006-01-02"),
	})
	taskRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, instanceURL+"/services/data/v62.0/sobjects/Task", strings.NewReader(string(taskPayload)))
	if err != nil {
		return nil, err
	}
	taskRequest.Header.Set("Authorization", "Bearer "+token.AccessToken)
	taskRequest.Header.Set("Content-Type", "application/json")
	taskResponse, err := addonDo(ctx, "salesforce", taskRequest)
	if err != nil {
		return nil, err
	}
	taskRaw, _ := io.ReadAll(io.LimitReader(taskResponse.Body, 512*1024))
	taskResponse.Body.Close()
	if taskResponse.StatusCode < 200 || taskResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Salesforce task create returned %d", taskResponse.StatusCode)
	}
	var task struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal(taskRaw, &task); err != nil || !task.Success {
		return nil, fmt.Errorf("Salesforce rejected the task")
	}
	return map[string]any{
		"contactId": contact.ID, "taskId": task.ID,
		"contactName": firstNonEmpty(contact.Name, fromEmail),
	}, nil
}

func freshdeskCreateTicket(ctx context.Context, domain, apiKey string, message addonMailMessage) (map[string]any, error) {
	if !validZendeskSubdomain(domain) {
		return nil, fmt.Errorf("Freshdesk domain is invalid")
	}
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" || !looksLikeEmail(fromEmail) {
		return nil, fmt.Errorf("Freshdesk needs the sender's email address")
	}
	payload, _ := json.Marshal(map[string]any{
		"subject":     firstNonEmpty(message.Subject, "Email from "+fromEmail),
		"description": addonProviderNote(message),
		"email":       fromEmail,
		"priority":    2,
		"status":      2,
		"tags":        []string{"alt8mail"},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+domain+".freshdesk.com/api/v2/tickets", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(apiKey, "X")
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "freshdesk", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Freshdesk ticket create returned %d", response.StatusCode)
	}
	var ticket struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &ticket); err != nil || ticket.ID == 0 {
		return nil, fmt.Errorf("Freshdesk returned an invalid ticket")
	}
	return map[string]any{
		"ticketId":  ticket.ID,
		"ticketUrl": "https://" + domain + ".freshdesk.com/a/tickets/" + strconv.FormatInt(ticket.ID, 10),
	}, nil
}

func intercomHost(region string) (string, bool) {
	switch region {
	case "us":
		return "api.intercom.io", true
	case "eu":
		return "api.eu.intercom.io", true
	case "au":
		return "api.au.intercom.io", true
	}
	return "", false
}

func intercomAddNote(ctx context.Context, region, accessToken string, message addonMailMessage) (map[string]any, error) {
	host, ok := intercomHost(region)
	if !ok {
		return nil, fmt.Errorf("Intercom region is invalid")
	}
	fromEmail := strings.ToLower(strings.TrimSpace(message.FromEmail))
	if fromEmail == "" || !looksLikeEmail(fromEmail) {
		return nil, fmt.Errorf("Intercom needs the sender's email address")
	}
	searchPayload, _ := json.Marshal(map[string]any{
		"query": map[string]string{"field": "email", "operator": "=", "value": fromEmail},
	})
	searchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/contacts/search", strings.NewReader(string(searchPayload)))
	if err != nil {
		return nil, err
	}
	searchRequest.Header.Set("Authorization", "Bearer "+accessToken)
	searchRequest.Header.Set("Intercom-Version", "2.11")
	searchRequest.Header.Set("Content-Type", "application/json")
	searchResponse, err := addonDo(ctx, "intercom", searchRequest)
	if err != nil {
		return nil, err
	}
	searchRaw, _ := io.ReadAll(io.LimitReader(searchResponse.Body, 512*1024))
	searchResponse.Body.Close()
	if searchResponse.StatusCode < 200 || searchResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Intercom contact search returned %d", searchResponse.StatusCode)
	}
	var search struct {
		Data []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(searchRaw, &search); err != nil || len(search.Data) == 0 || search.Data[0].ID == "" {
		return nil, fmt.Errorf("no Intercom contact found for %s", fromEmail)
	}
	contact := search.Data[0]
	notePayload, _ := json.Marshal(map[string]string{"body": addonProviderNote(message)})
	noteRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/contacts/"+url.PathEscape(contact.ID)+"/notes", strings.NewReader(string(notePayload)))
	if err != nil {
		return nil, err
	}
	noteRequest.Header.Set("Authorization", "Bearer "+accessToken)
	noteRequest.Header.Set("Intercom-Version", "2.11")
	noteRequest.Header.Set("Content-Type", "application/json")
	noteResponse, err := addonDo(ctx, "intercom", noteRequest)
	if err != nil {
		return nil, err
	}
	noteRaw, _ := io.ReadAll(io.LimitReader(noteResponse.Body, 512*1024))
	noteResponse.Body.Close()
	if noteResponse.StatusCode < 200 || noteResponse.StatusCode >= 300 {
		return nil, fmt.Errorf("Intercom note create returned %d", noteResponse.StatusCode)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(noteRaw, &note); err != nil || note.ID == "" {
		return nil, fmt.Errorf("Intercom returned an invalid note")
	}
	return map[string]any{
		"contactId": contact.ID, "noteId": note.ID,
		"contactName": firstNonEmpty(contact.Name, contact.Email, fromEmail),
	}, nil
}

func tickTickCreateTask(ctx context.Context, accessToken, projectID string, message addonMailMessage) (map[string]any, error) {
	if !validTickTickProject(projectID) {
		return nil, fmt.Errorf("TickTick project ID is invalid")
	}
	payload, _ := json.Marshal(map[string]any{
		"title":     firstNonEmpty(message.Subject, "Email task"),
		"content":   addonProviderNote(message),
		"projectId": projectID,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.ticktick.com/open/v1/task", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "ticktick", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("TickTick task create returned %d", response.StatusCode)
	}
	var task struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		return nil, fmt.Errorf("TickTick returned an invalid task")
	}
	return map[string]any{
		"taskId":  task.ID,
		"taskUrl": "https://ticktick.com/webapp/#p/" + url.PathEscape(projectID) + "/tasks/" + url.PathEscape(task.ID),
	}, nil
}

func calendlyCreateLink(ctx context.Context, accessToken, eventTypeURI, ownerURI string) (map[string]any, error) {
	if !validCalendlyURI(eventTypeURI, "/event_types/") || !validCalendlyURI(ownerURI, "/users/") {
		return nil, fmt.Errorf("Calendly event type or owner URI is invalid")
	}
	payload, _ := json.Marshal(map[string]any{
		"max_event_count": 1,
		"owner":           ownerURI,
		"owner_type":      "User",
		"event_type":      eventTypeURI,
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.calendly.com/scheduling_links", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "calendly", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Calendly link create returned %d", response.StatusCode)
	}
	var result struct {
		Resource struct {
			BookingURL string `json:"booking_url"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Resource.BookingURL == "" {
		return nil, fmt.Errorf("Calendly returned an invalid scheduling link")
	}
	return map[string]any{"bookingUrl": result.Resource.BookingURL}, nil
}

func jiraCreateIssue(ctx context.Context, site, email, apiToken, projectKey, issueType string, message addonMailMessage) (map[string]any, error) {
	if !validJiraSite(site) || !looksLikeEmail(email) || !validJiraProjectKey(projectKey) {
		return nil, fmt.Errorf("Jira configuration is invalid")
	}
	if issueType == "" {
		issueType = "Task"
	}
	description := map[string]any{
		"type": "doc", "version": 1,
		"content": []any{map[string]any{
			"type": "paragraph",
			"content": []any{map[string]any{
				"type": "text", "text": addonProviderNote(message),
			}},
		}},
	}
	payload, _ := json.Marshal(map[string]any{
		"fields": map[string]any{
			"project":     map[string]string{"key": projectKey},
			"summary":     firstNonEmpty(message.Subject, "Email issue"),
			"description": description,
			"issuetype":   map[string]string{"name": issueType},
		},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+site+"/rest/api/3/issue", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(email, apiToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "jira", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Jira issue create returned %d", response.StatusCode)
	}
	var issue struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &issue); err != nil || issue.Key == "" {
		return nil, fmt.Errorf("Jira returned an invalid issue")
	}
	return map[string]any{
		"issueId": issue.ID, "issueKey": issue.Key,
		"issueUrl": "https://" + site + "/browse/" + issue.Key,
	}, nil
}

func linearCreateIssue(ctx context.Context, apiKey, teamID string, message addonMailMessage) (map[string]any, error) {
	if !validLinearTeam(teamID) {
		return nil, fmt.Errorf("Linear team ID is invalid")
	}
	payload, _ := json.Marshal(map[string]any{
		"query": `mutation IssueCreate($input: IssueCreateInput!) {
			issueCreate(input: $input) { success issue { id identifier url } }
		}`,
		"variables": map[string]any{"input": map[string]any{
			"teamId":      teamID,
			"title":       firstNonEmpty(message.Subject, "Email issue"),
			"description": addonProviderNote(message),
		}},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.linear.app/graphql", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "linear", request)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Linear issue create returned %d", response.StatusCode)
	}
	var result struct {
		Data struct {
			IssueCreate struct {
				Success bool `json:"success"`
				Issue   struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					URL        string `json:"url"`
				} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.Data.IssueCreate.Success || result.Data.IssueCreate.Issue.ID == "" {
		errText := "Linear rejected the issue"
		if len(result.Errors) > 0 && result.Errors[0].Message != "" {
			errText += ": " + result.Errors[0].Message
		}
		return nil, fmt.Errorf("%s", errText)
	}
	issue := result.Data.IssueCreate.Issue
	return map[string]any{
		"issueId": issue.ID, "issueKey": issue.Identifier, "issueUrl": issue.URL,
	}, nil
}

func slackPost(ctx context.Context, webhookURL string, message addonMailMessage) error {
	if err := validateSlackWebhook(webhookURL); err != nil {
		return err
	}
	from := strings.TrimSpace(firstNonEmpty(message.FromName, message.FromEmail))
	subject := strings.TrimSpace(message.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	preview := strings.TrimSpace(firstNonEmpty(message.Snippet, message.BodyText))
	if len(preview) > 1200 {
		preview = preview[:1200]
	}
	text := fmt.Sprintf("*New email from %s*\n*Subject:* %s\n> %s", slackEscape(from), slackEscape(subject), slackEscape(preview))
	payload, _ := json.Marshal(map[string]string{"text": text})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := addonDo(ctx, "slack", request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 16*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Slack returned %d", response.StatusCode)
	}
	if body := strings.TrimSpace(string(raw)); body != "" && body != "ok" {
		return fmt.Errorf("Slack rejected the webhook")
	}
	return nil
}

func slackEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

var addonHTTPClient = &http.Client{Timeout: 15 * time.Second}
