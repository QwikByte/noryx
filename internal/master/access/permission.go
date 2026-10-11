// Package access decides what users of the panel may do. Users get permissions through
// groups; a group's node and server permissions apply to all servers or only to the nodes
// and servers of its scope. The built-in Administrators group has every permission.
// Every API route states the permission it needs when it is registered with Mux.
package access

import "slices"

// Permission allows an action in the panel.
type Permission string

const (
	NodesView         Permission = "nodes.view"
	NodesEdit         Permission = "nodes.edit"
	NodesCertificates Permission = "nodes.certificates"
	NodesDelete       Permission = "nodes.delete"
	NodesEnroll       Permission = "nodes.enroll"
	OverlayManage     Permission = "overlay.manage"

	ServersView     Permission = "servers.view"
	ServersCreate   Permission = "servers.create"
	ServersStart    Permission = "servers.start"
	ServersStop     Permission = "servers.stop"
	ServersRestart  Permission = "servers.restart"
	ServersSettings Permission = "servers.settings"
	ServersDelete   Permission = "servers.delete"

	ConsoleView     Permission = "console.view"
	ConsoleCommands Permission = "console.commands"

	PlayersManage Permission = "players.manage"

	FilesRead  Permission = "files.read"
	FilesWrite Permission = "files.write"
	Properties Permission = "properties.edit"
	Plugins    Permission = "plugins.manage"

	BackupsView    Permission = "backups.view"
	BackupsCreate  Permission = "backups.create"
	BackupsRestore Permission = "backups.restore"
	BackupsDelete  Permission = "backups.delete"

	NetworksView     Permission = "networks.view"
	NetworksManage   Permission = "networks.manage"
	DatastoresView   Permission = "datastores.view"
	DatastoresManage Permission = "datastores.manage"
	TemplatesView    Permission = "templates.view"
	TemplatesManage  Permission = "templates.manage"
	FileSetsView     Permission = "filesets.view"
	FileSetsManage   Permission = "filesets.manage"
	BackupJobsView   Permission = "backupjobs.view"
	BackupJobsManage Permission = "backupjobs.manage"
	PoliciesView     Permission = "policies.view"
	PoliciesManage   Permission = "policies.manage"
	WorkflowsView    Permission = "workflows.view"
	WorkflowsManage  Permission = "workflows.manage"

	LogsView            Permission = "logs.view"
	NotificationsManage Permission = "notifications.manage"

	SettingsView Permission = "settings.view"
	SettingsEdit Permission = "settings.edit"
	Terminal     Permission = "terminal.use"
	UsersView    Permission = "users.view"
	UsersManage  Permission = "users.manage"
	GroupsManage Permission = "groups.manage"
)

// Administrators is no permission but the built-in group, for what only its members may do,
// e.g. updating Noryx. Groups can't be given it.
const Administrators Permission = "administrators"

// Info describes a permission for the panel.
type Info struct {
	ID          Permission `json:"id"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	// Scoped permissions only apply to the nodes and servers in the scope of a group;
	// the others apply everywhere.
	Scoped bool `json:"scoped"`
	// Requires are granted along with the permission, e.g. seeing the servers one may start.
	Requires []Permission `json:"requires,omitempty"`
}

// Area is a part of the panel and the permissions for it.
type Area struct {
	Name        string `json:"name"`
	Permissions []Info `json:"permissions"`
}

func scoped(id Permission, label, description string, requires ...Permission) Info {
	return Info{id, label, description, true, requires}
}

func global(id Permission, label, description string, requires ...Permission) Info {
	return Info{id, label, description, false, requires}
}

// Catalog lists all permissions by area.
var Catalog = []Area{
	{"Nodes", []Info{
		scoped(NodesView, "See nodes", "Their machines, agents and storage locations."),
		scoped(NodesEdit, "Change nodes", "Name, agent address, default storage, port range and memory limit.", NodesView),
		scoped(NodesCertificates, "Renew certificates", "Renew the certificate of a node before it is due.", NodesView),
		scoped(NodesDelete, "Remove nodes", "The panel stops managing them; their servers keep running. Taking their servers out of networks first also needs the permission to manage networks.", NodesView),
		scoped(NodesEnroll, "Add nodes", "Add nodes and create join tokens for agents. Only in groups for all servers, as new nodes are outside other scopes.", NodesView),
		global(OverlayManage, "Manage the private network", "Add nodes whose administrator allowed it to the private network of the nodes, remove them and rotate their keys, on the pages of the nodes one may see. Applies to all nodes, as members reach the ports of the others."),
	}},
	{"Servers", []Info{
		scoped(ServersView, "See servers", "Servers with their state, settings and plugins."),
		scoped(ServersCreate, "Create servers", "On whole nodes of the scope, also from templates; their plugins also need the permission to manage plugins, their tags the one to change server settings. A server created from an archive brings its plugins, which run with the server and can read its secrets.", ServersView),
		scoped(ServersStart, "Start servers", "", ServersView),
		scoped(ServersStop, "Stop servers", "", ServersView),
		scoped(ServersRestart, "Restart servers", "", ServersView),
		scoped(ServersSettings, "Change server settings", "Name, tags, notes, version, memory, port, Java, CPU limit, stop timeout and time zone. Changes other than tags and notes restart the server.", ServersView),
		scoped(ServersDelete, "Delete servers", "With all their data and backups.", ServersView),
	}},
	{"Console", []Info{
		scoped(ConsoleView, "Read the console", "The live output of servers.", ServersView),
		scoped(ConsoleCommands, "Send console commands", "Run any command of the server, e.g. op.", ConsoleView),
	}},
	{"Players", []Info{
		scoped(PlayersManage, "Manage players", "Kick and ban players, change the whitelist and send players to other servers of a network. Making players operators also needs the permission to send console commands.", ServersView),
	}},
	{"Files and configuration", []Info{
		scoped(FilesRead, "Browse and download files", "Secrets such as the RCON password stay hidden. Also needed to duplicate a server, as the copy contains its files.", ServersView),
		scoped(FilesWrite, "Change files", "Upload, edit, move and delete files. Uploaded plugins run with the server and can read its secrets, such as the forwarding secret of its network. Also needed to upload backups, which can bring any file.", FilesRead),
		scoped(Properties, "Edit server.properties", "", ServersView),
		scoped(Plugins, "Manage plugins and mods", "Install, update, upload and remove them. They run with the server and can read its secrets.", ServersView),
	}},
	{"Backups", []Info{
		scoped(BackupsView, "See and download backups", "Downloads contain all backed up files, without secrets such as the RCON password.", ServersView),
		scoped(BackupsCreate, "Back up servers", "Also change the labels of backups, and keep backups of jobs from being deleted.", BackupsView),
		scoped(BackupsRestore, "Restore backups", "Replaces the backed up data; a running server restarts. Also restore backups of other servers one may see into these.", BackupsView),
		scoped(BackupsDelete, "Delete backups", "Also let jobs delete backups that were kept.", BackupsView),
	}},
	{"Networks, databases, templates and file sets", []Info{
		global(NetworksView, "See networks", ""),
		global(NetworksManage, "Manage networks", "Create, change and delete networks, which configures and restarts their servers.", NetworksView),
		global(DatastoresView, "See databases", "The MariaDB and PostgreSQL datastores of networks with their databases, health, addresses and dumps. Never their passwords or data."),
		global(DatastoresManage, "Manage databases", "Create, change, upgrade and delete datastores and their databases. See and rotate the passwords of the databases, put them into file sets and look into their tables. Back up, restore and download dumps, which contain all data of the databases.", DatastoresView),
		global(TemplatesView, "See templates", "Creating a server from a template also needs the permission to create servers."),
		global(TemplatesManage, "Manage templates", "Create, change and delete templates.", TemplatesView),
		global(FileSetsView, "See file sets", "Their files, targets and variables, the names of their secrets, and which servers have them."),
		global(FileSetsManage, "Manage file sets", "Create, change and delete file sets and set their secrets. File sets configure plugins, which run code and can read the secrets of the sets. Applying a set also needs the permission to change the files of each server it is for, and passwords of databases in a set need the permission to manage databases.", FileSetsView),
	}},
	{"Backup jobs, schedules and workflows", []Info{
		global(BackupJobsView, "See backup jobs", ""),
		global(BackupJobsManage, "Manage backup jobs", "Create, change, delete and run backup jobs for any server. Copying their backups away from the nodes, and managing the storage for the copies, also needs the permission to see and download backups on all servers.", BackupJobsView),
		global(PoliciesView, "See schedules", ""),
		global(PoliciesManage, "Manage schedules", "Create, change, delete and run schedules, which restart, stop and start any server or run console commands.", PoliciesView),
		global(WorkflowsView, "See workflows", "Their triggers and steps, and their runs with what their triggers and steps told, e.g. entries of the log, players and the answers of requests, about any server. Never the secret headers of requests or the URLs of webhooks."),
		global(WorkflowsManage, "Manage workflows", "Create, change, delete, run and cancel workflows, and create the URLs that start them. Each step needs its own permission on all servers, e.g. to restart servers or send console commands, which the user who saved a workflow last needs for each run; requests to the internet and messages to notification channels need the permission to manage notifications.", WorkflowsView),
	}},
	{"Logs", []Info{
		scoped(LogsView, "See logs", "Actions, warnings and errors of the master and the agents. Entries about users, groups, settings and the master itself need it for all servers."),
		global(NotificationsManage, "Manage notifications", "Send entries of the log to Discord, Slack, webhooks and by mail, with rules that apply to all nodes and servers, so it also needs the permission to see logs for all servers. The addresses of webhooks and the passwords of mail servers can be set but never seen."),
	}},
	{"System", []Info{
		global(SettingsView, "See the master's settings", "Its version, addresses and settings."),
		global(SettingsEdit, "Change the master's settings", "Enrollment address, join tokens, sessions, who has to use two-factor authentication, limits of new nodes, what new servers start with, the defaults of users, the name of the panel and how players are warned; the texts of warnings also need console commands on all servers.", SettingsView),
		global(Terminal, "Use the terminal", "Each command also needs its own permission, e.g. to restart a server."),
	}},
	{"Users and groups", []Info{
		global(UsersView, "See users and groups", ""),
		global(UsersManage, "Manage users", "Invite, disable and delete users, create setup links and choose their groups. Only for users without more permissions than oneself.", UsersView),
		global(GroupsManage, "Manage groups", "Create, change and delete groups. Only with permissions one has oneself.", UsersView),
	}},
}

// Label returns how the panel names a permission, e.g. "Back up servers".
func Label(p Permission) string {
	info, _ := lookup(p)
	return info.Label
}

// lookup finds the description of a permission.
func lookup(p Permission) (Info, bool) {
	for _, a := range Catalog {
		if i := slices.IndexFunc(a.Permissions, func(info Info) bool { return info.ID == p }); i >= 0 {
			return a.Permissions[i], true
		}
	}
	return Info{}, false
}

// withRequired adds the permissions that the given ones require, sorted and without duplicates.
func withRequired(perms []Permission) []Permission {
	all := slices.Clone(perms)
	for i := 0; i < len(all); i++ {
		info, _ := lookup(all[i])
		for _, r := range info.Requires {
			if !slices.Contains(all, r) {
				all = append(all, r)
			}
		}
	}
	slices.Sort(all)
	return slices.Compact(all)
}
