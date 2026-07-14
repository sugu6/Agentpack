export namespace agents {
	
	export class Agent {
	    id: string;
	    name: string;
	    type: string;
	    variant: string;
	    status: string;
	    configPath: string;
	    configFormat: string;
	    mcpCount: number;
	    detectedAt: string;
	    lastScannedAt: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new Agent(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.type = source["type"];
	        this.variant = source["variant"];
	        this.status = source["status"];
	        this.configPath = source["configPath"];
	        this.configFormat = source["configFormat"];
	        this.mcpCount = source["mcpCount"];
	        this.detectedAt = source["detectedAt"];
	        this.lastScannedAt = source["lastScannedAt"];
	        this.error = source["error"];
	    }
	}

}

export namespace backup {
	
	export class ImportOptions {
	    ApplyMCP: boolean;
	    Overwrite: boolean;
	    ApplyAgentStatus: boolean;
	    ApplySettings: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ImportOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ApplyMCP = source["ApplyMCP"];
	        this.Overwrite = source["Overwrite"];
	        this.ApplyAgentStatus = source["ApplyAgentStatus"];
	        this.ApplySettings = source["ApplySettings"];
	    }
	}
	export class ImportResult {
	    mcpApplied: number;
	    mcpSkipped: number;
	    agentStatusApplied: number;
	    exportedSettings?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new ImportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mcpApplied = source["mcpApplied"];
	        this.mcpSkipped = source["mcpSkipped"];
	        this.agentStatusApplied = source["agentStatusApplied"];
	        this.exportedSettings = source["exportedSettings"];
	    }
	}
	export class SnapshotMCP {
	    name: string;
	    description: string;
	    command: string;
	    args: string[];
	    env: Record<string, string>;
	    transport: string;
	    url: string;
	    source: string;
	    sourceId: string;
	    boundAgents?: string[];
	
	    static createFrom(source: any = {}) {
	        return new SnapshotMCP(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.description = source["description"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.transport = source["transport"];
	        this.url = source["url"];
	        this.source = source["source"];
	        this.sourceId = source["sourceId"];
	        this.boundAgents = source["boundAgents"];
	    }
	}
	export class Snapshot {
	    version: number;
	    schemaVersion: number;
	    createdAt: string;
	    action?: string;
	    agentId?: string;
	    agentPath?: string;
	    description?: string;
	    settings?: Record<string, any>;
	    mcpServers?: SnapshotMCP[];
	
	    static createFrom(source: any = {}) {
	        return new Snapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.schemaVersion = source["schemaVersion"];
	        this.createdAt = source["createdAt"];
	        this.action = source["action"];
	        this.agentId = source["agentId"];
	        this.agentPath = source["agentPath"];
	        this.description = source["description"];
	        this.settings = source["settings"];
	        this.mcpServers = this.convertValues(source["mcpServers"], SnapshotMCP);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class Summary {
	    id: string;
	    createdAt: string;
	    description: string;
	    action: string;
	    agentId: string;
	    agentPath: string;
	    mcpCount: number;
	    version: string;
	
	    static createFrom(source: any = {}) {
	        return new Summary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.createdAt = source["createdAt"];
	        this.description = source["description"];
	        this.action = source["action"];
	        this.agentId = source["agentId"];
	        this.agentPath = source["agentPath"];
	        this.mcpCount = source["mcpCount"];
	        this.version = source["version"];
	    }
	}

}

export namespace config {
	
	export class MarketSource {
	    enabled: boolean;
	    lastSync?: number;
	
	    static createFrom(source: any = {}) {
	        return new MarketSource(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.lastSync = source["lastSync"];
	    }
	}
	export class SkillRepo {
	    owner: string;
	    name: string;
	    branch: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillRepo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.owner = source["owner"];
	        this.name = source["name"];
	        this.branch = source["branch"];
	    }
	}
	export class Settings {
	    theme: string;
	    marketSources: Record<string, MarketSource>;
	    autoBackup: boolean;
	    backupCount: number;
	    backupRetention: number;
	    skillStorage: string;
	    skillSyncMethod: string;
	    skillRepos: SkillRepo[];
	    windowAction: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.marketSources = this.convertValues(source["marketSources"], MarketSource, true);
	        this.autoBackup = source["autoBackup"];
	        this.backupCount = source["backupCount"];
	        this.backupRetention = source["backupRetention"];
	        this.skillStorage = source["skillStorage"];
	        this.skillSyncMethod = source["skillSyncMethod"];
	        this.skillRepos = this.convertValues(source["skillRepos"], SkillRepo);
	        this.windowAction = source["windowAction"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class UpdateCheckResult {
	    hasUpdate: boolean;
	    currentVersion: string;
	    latestVersion: string;
	    message: string;
	    changelog: string;
	    releaseUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateCheckResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasUpdate = source["hasUpdate"];
	        this.currentVersion = source["currentVersion"];
	        this.latestVersion = source["latestVersion"];
	        this.message = source["message"];
	        this.changelog = source["changelog"];
	        this.releaseUrl = source["releaseUrl"];
	    }
	}

}

export namespace market {
	
	export class MarketServer {
	    id: string;
	    name: string;
	    title?: string;
	    description: string;
	    homepage?: string;
	    docs?: string;
	    tags: string[];
	    transport?: string;
	    command?: string;
	    args?: string[];
	    env?: Record<string, string>;
	    url?: string;
	    source: string;
	    sourceId: string;
	    installs?: number;
	    stars?: number;
	    updatedAt: string;
	    bySmithery?: boolean;
	    isDeployed?: boolean;
	    isVerified?: boolean;
	    isRemote?: boolean;
	    registry?: string;
	
	    static createFrom(source: any = {}) {
	        return new MarketServer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.homepage = source["homepage"];
	        this.docs = source["docs"];
	        this.tags = source["tags"];
	        this.transport = source["transport"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.url = source["url"];
	        this.source = source["source"];
	        this.sourceId = source["sourceId"];
	        this.installs = source["installs"];
	        this.stars = source["stars"];
	        this.updatedAt = source["updatedAt"];
	        this.bySmithery = source["bySmithery"];
	        this.isDeployed = source["isDeployed"];
	        this.isVerified = source["isVerified"];
	        this.isRemote = source["isRemote"];
	        this.registry = source["registry"];
	    }
	}
	export class MarketSkill {
	    id: string;
	    name: string;
	    description: string;
	    directory: string;
	    fullPath?: string;
	    source: string;
	    sourceId: string;
	    installs: number;
	    repoOwner: string;
	    repoName: string;
	    repoBranch: string;
	    readmeUrl?: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new MarketSkill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.directory = source["directory"];
	        this.fullPath = source["fullPath"];
	        this.source = source["source"];
	        this.sourceId = source["sourceId"];
	        this.installs = source["installs"];
	        this.repoOwner = source["repoOwner"];
	        this.repoName = source["repoName"];
	        this.repoBranch = source["repoBranch"];
	        this.readmeUrl = source["readmeUrl"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class SearchResultServers {
	    items: MarketServer[];
	    total: number;
	    page: number;
	    hasMore: boolean;
	    nextPage?: string;
	
	    static createFrom(source: any = {}) {
	        return new SearchResultServers(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], MarketServer);
	        this.total = source["total"];
	        this.page = source["page"];
	        this.hasMore = source["hasMore"];
	        this.nextPage = source["nextPage"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SourceStatus {
	    source: string;
	    status: string;
	    count: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new SourceStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.status = source["status"];
	        this.count = source["count"];
	        this.error = source["error"];
	    }
	}
	export class SearchResultSkills {
	    items: MarketSkill[];
	    total: number;
	    page: number;
	    hasMore: boolean;
	    nextPage?: string;
	    sourceStatuses?: SourceStatus[];
	
	    static createFrom(source: any = {}) {
	        return new SearchResultSkills(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], MarketSkill);
	        this.total = source["total"];
	        this.page = source["page"];
	        this.hasMore = source["hasMore"];
	        this.nextPage = source["nextPage"];
	        this.sourceStatuses = this.convertValues(source["sourceStatuses"], SourceStatus);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace mcp {
	
	export class Server {
	    id: string;
	    name: string;
	    description?: string;
	    command: string;
	    args: string[];
	    env?: Record<string, string>;
	    cwd?: string;
	    transport: string;
	    configType?: string;
	    url?: string;
	    timeout?: number;
	    source: string;
	    sourceId?: string;
	    boundAgents: string[];
	    installedAt: string;
	    updatedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new Server(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.command = source["command"];
	        this.args = source["args"];
	        this.env = source["env"];
	        this.cwd = source["cwd"];
	        this.transport = source["transport"];
	        this.configType = source["configType"];
	        this.url = source["url"];
	        this.timeout = source["timeout"];
	        this.source = source["source"];
	        this.sourceId = source["sourceId"];
	        this.boundAgents = source["boundAgents"];
	        this.installedAt = source["installedAt"];
	        this.updatedAt = source["updatedAt"];
	    }
	}
	export class ScanItem {
	    server: Server;
	    managed: boolean;
	    agentId: string;
	    agentName: string;
	    configPath: string;
	
	    static createFrom(source: any = {}) {
	        return new ScanItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.server = this.convertValues(source["server"], Server);
	        this.managed = source["managed"];
	        this.agentId = source["agentId"];
	        this.agentName = source["agentName"];
	        this.configPath = source["configPath"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ScanResult {
	    items: ScanItem[];
	    total: number;
	    managed: number;
	    newFound: number;
	
	    static createFrom(source: any = {}) {
	        return new ScanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], ScanItem);
	        this.total = source["total"];
	        this.managed = source["managed"];
	        this.newFound = source["newFound"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace skills {
	
	export class AdoptedSkill {
	    directory: string;
	    agentIds: string[];
	
	    static createFrom(source: any = {}) {
	        return new AdoptedSkill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory = source["directory"];
	        this.agentIds = source["agentIds"];
	    }
	}
	export class SkillConflict {
	    directory: string;
	    agentIds: string[];
	
	    static createFrom(source: any = {}) {
	        return new SkillConflict(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory = source["directory"];
	        this.agentIds = source["agentIds"];
	    }
	}
	export class AdoptionResult {
	    adopted: AdoptedSkill[];
	    conflicts: SkillConflict[];
	    errors: string[];
	
	    static createFrom(source: any = {}) {
	        return new AdoptionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.adopted = this.convertValues(source["adopted"], AdoptedSkill);
	        this.conflicts = this.convertValues(source["conflicts"], SkillConflict);
	        this.errors = source["errors"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MigrationResult {
	    migrated: number;
	    errors?: string[];
	
	    static createFrom(source: any = {}) {
	        return new MigrationResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.migrated = source["migrated"];
	        this.errors = source["errors"];
	    }
	}
	export class Skill {
	    id: string;
	    name: string;
	    description?: string;
	    directory: string;
	    contentHash?: string;
	    boundAgents: string[];
	    installedAt: string;
	    updatedAt: string;
	    repoOwner?: string;
	    repoName?: string;
	    repoBranch?: string;
	
	    static createFrom(source: any = {}) {
	        return new Skill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.directory = source["directory"];
	        this.contentHash = source["contentHash"];
	        this.boundAgents = source["boundAgents"];
	        this.installedAt = source["installedAt"];
	        this.updatedAt = source["updatedAt"];
	        this.repoOwner = source["repoOwner"];
	        this.repoName = source["repoName"];
	        this.repoBranch = source["repoBranch"];
	    }
	}
	
	export class UninstallResult {
	    id: string;
	    backupPath?: string;
	
	    static createFrom(source: any = {}) {
	        return new UninstallResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.backupPath = source["backupPath"];
	    }
	}
	export class UnmanagedSkill {
	    agentId: string;
	    directory: string;
	    path: string;
	    name?: string;
	    foundIn?: string[];
	
	    static createFrom(source: any = {}) {
	        return new UnmanagedSkill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.agentId = source["agentId"];
	        this.directory = source["directory"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.foundIn = source["foundIn"];
	    }
	}
	export class UpdateStatus {
	    skillId: string;
	    directory: string;
	    localHash: string;
	    remoteHash: string;
	    hasUpdate: boolean;
	    checkedAt: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.skillId = source["skillId"];
	        this.directory = source["directory"];
	        this.localHash = source["localHash"];
	        this.remoteHash = source["remoteHash"];
	        this.hasUpdate = source["hasUpdate"];
	        this.checkedAt = source["checkedAt"];
	        this.error = source["error"];
	    }
	}

}

