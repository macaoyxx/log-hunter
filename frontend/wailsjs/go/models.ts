export namespace appconf {
	
	export class HistItem {
	    label: string;
	    spec: string;
	
	    static createFrom(source: any = {}) {
	        return new HistItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.spec = source["spec"];
	    }
	}
	export class ThemeSettings {
	    appearance: string;
	    palette: string;
	
	    static createFrom(source: any = {}) {
	        return new ThemeSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.appearance = source["appearance"];
	        this.palette = source["palette"];
	    }
	}
	export class Settings {
	    theme: ThemeSettings;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = this.convertValues(source["theme"], ThemeSettings);
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
	export class Conf {
	    settings: Settings;
	    history: HistItem[];
	    theme?: string;
	
	    static createFrom(source: any = {}) {
	        return new Conf(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], Settings);
	        this.history = this.convertValues(source["history"], HistItem);
	        this.theme = source["theme"];
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

export namespace conf {
	
	export class Model {
	    name: string;
	    ctx?: number;
	    vision?: boolean;
	    maxOut?: number;
	
	    static createFrom(source: any = {}) {
	        return new Model(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.ctx = source["ctx"];
	        this.vision = source["vision"];
	        this.maxOut = source["maxOut"];
	    }
	}
	export class Profile {
	    id: string;
	    name: string;
	    baseUrl: string;
	    apiKey: string;
	    models: Model[];
	
	    static createFrom(source: any = {}) {
	        return new Profile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.baseUrl = source["baseUrl"];
	        this.apiKey = source["apiKey"];
	        this.models = this.convertValues(source["models"], Model);
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
	export class Conf {
	    profiles: Profile[];
	    activeProfile?: string;
	    activeModel?: string;
	    visionProfile?: string;
	    visionModel?: string;
	    effort?: string;
	    ctxTokens?: number;
	    maxOutTokens?: number;
	    grantedDirs?: string[];
	
	    static createFrom(source: any = {}) {
	        return new Conf(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profiles = this.convertValues(source["profiles"], Profile);
	        this.activeProfile = source["activeProfile"];
	        this.activeModel = source["activeModel"];
	        this.visionProfile = source["visionProfile"];
	        this.visionModel = source["visionModel"];
	        this.effort = source["effort"];
	        this.ctxTokens = source["ctxTokens"];
	        this.maxOutTokens = source["maxOutTokens"];
	        this.grantedDirs = source["grantedDirs"];
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

export namespace logscan {
	
	export class ClusterSample {
	    idx: number;
	    file: string;
	    fileId: number;
	    lineNo: number;
	    offset: number;
	    ts: number;
	    level: number;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new ClusterSample(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.idx = source["idx"];
	        this.file = source["file"];
	        this.fileId = source["fileId"];
	        this.lineNo = source["lineNo"];
	        this.offset = source["offset"];
	        this.ts = source["ts"];
	        this.level = source["level"];
	        this.text = source["text"];
	    }
	}
	export class ClusterGroup {
	    template: string;
	    count: number;
	    level: number;
	    firstTs: number;
	    lastTs: number;
	    firstIdx: number;
	    samples: ClusterSample[];
	
	    static createFrom(source: any = {}) {
	        return new ClusterGroup(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.template = source["template"];
	        this.count = source["count"];
	        this.level = source["level"];
	        this.firstTs = source["firstTs"];
	        this.lastTs = source["lastTs"];
	        this.firstIdx = source["firstIdx"];
	        this.samples = this.convertValues(source["samples"], ClusterSample);
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
	export class ClusterReport {
	    groups: ClusterGroup[];
	    totalRows: number;
	    participated: number;
	    sampled: boolean;
	    costMs: number;
	
	    static createFrom(source: any = {}) {
	        return new ClusterReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.groups = this.convertValues(source["groups"], ClusterGroup);
	        this.totalRows = source["totalRows"];
	        this.participated = source["participated"];
	        this.sampled = source["sampled"];
	        this.costMs = source["costMs"];
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
	
	export class ContextRow {
	    offset: number;
	    lineNo: number;
	    ts: number;
	    level: number;
	    text: string;
	    isTarget: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ContextRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.offset = source["offset"];
	        this.lineNo = source["lineNo"];
	        this.ts = source["ts"];
	        this.level = source["level"];
	        this.text = source["text"];
	        this.isTarget = source["isTarget"];
	    }
	}
	export class FileEntry {
	    id: number;
	    path: string;
	    name: string;
	    size: number;
	    enc: string;
	    format: string;
	    hasTime: boolean;
	    lineCnt: number;
	    tsMin: number;
	    tsMax: number;
	
	    static createFrom(source: any = {}) {
	        return new FileEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.enc = source["enc"];
	        this.format = source["format"];
	        this.hasTime = source["hasTime"];
	        this.lineCnt = source["lineCnt"];
	        this.tsMin = source["tsMin"];
	        this.tsMax = source["tsMax"];
	    }
	}
	export class Histogram {
	    start: number;
	    step: number;
	    buckets: number[][];
	    maxTotal: number;
	    noTs: number;
	    hasData: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Histogram(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.start = source["start"];
	        this.step = source["step"];
	        this.buckets = source["buckets"];
	        this.maxTotal = source["maxTotal"];
	        this.noTs = source["noTs"];
	        this.hasData = source["hasData"];
	    }
	}
	export class QueryStats {
	    total: number;
	    truncated: boolean;
	    costMs: number;
	    levelCounts: number[];
	    maxLineLen: number;
	    skippedNoTime: number;
	    resultLimit: number;
	
	    static createFrom(source: any = {}) {
	        return new QueryStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.truncated = source["truncated"];
	        this.costMs = source["costMs"];
	        this.levelCounts = source["levelCounts"];
	        this.maxLineLen = source["maxLineLen"];
	        this.skippedNoTime = source["skippedNoTime"];
	        this.resultLimit = source["resultLimit"];
	    }
	}
	export class ResultRow {
	    fileId: number;
	    lineNo: number;
	    offset: number;
	    ts: number;
	    level: number;
	    preview: string;
	
	    static createFrom(source: any = {}) {
	        return new ResultRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileId = source["fileId"];
	        this.lineNo = source["lineNo"];
	        this.offset = source["offset"];
	        this.ts = source["ts"];
	        this.level = source["level"];
	        this.preview = source["preview"];
	    }
	}
	export class SideRow {
	    file: string;
	    fileId: number;
	    lineNo: number;
	    offset: number;
	    ts: number;
	    level: number;
	    text: string;
	
	    static createFrom(source: any = {}) {
	        return new SideRow(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.file = source["file"];
	        this.fileId = source["fileId"];
	        this.lineNo = source["lineNo"];
	        this.offset = source["offset"];
	        this.ts = source["ts"];
	        this.level = source["level"];
	        this.text = source["text"];
	    }
	}

}

export namespace main {
	
	export class AddFilesResult {
	    files: logscan.FileEntry[];
	    errors: string[];
	    notices: string[];
	    allFiles: logscan.FileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new AddFilesResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.files = this.convertValues(source["files"], logscan.FileEntry);
	        this.errors = source["errors"];
	        this.notices = source["notices"];
	        this.allFiles = this.convertValues(source["allFiles"], logscan.FileEntry);
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
	export class AiReportOut {
	    text: string;
	    docs: string[];
	
	    static createFrom(source: any = {}) {
	        return new AiReportOut(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.text = source["text"];
	        this.docs = source["docs"];
	    }
	}
	export class MemUsageInfo {
	    ws: number;
	    sysUsed: number;
	    sysTotal: number;
	
	    static createFrom(source: any = {}) {
	        return new MemUsageInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ws = source["ws"];
	        this.sysUsed = source["sysUsed"];
	        this.sysTotal = source["sysTotal"];
	    }
	}
	export class QuerySpecJSON {
	    fileIds: number[];
	    keywords: string[];
	    excludes: string[];
	    regex: string;
	    keywordAnd: boolean;
	    tsStart: string;
	    tsEnd: string;
	    levels: number[];
	
	    static createFrom(source: any = {}) {
	        return new QuerySpecJSON(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileIds = source["fileIds"];
	        this.keywords = source["keywords"];
	        this.excludes = source["excludes"];
	        this.regex = source["regex"];
	        this.keywordAnd = source["keywordAnd"];
	        this.tsStart = source["tsStart"];
	        this.tsEnd = source["tsEnd"];
	        this.levels = source["levels"];
	    }
	}
	export class UpdateInfo {
	    latest: string;
	    hasUpdate: boolean;
	    err: string;
	    assetUrl: string;
	    assetName: string;
	    assetSize: number;
	    sha256Url: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.latest = source["latest"];
	        this.hasUpdate = source["hasUpdate"];
	        this.err = source["err"];
	        this.assetUrl = source["assetUrl"];
	        this.assetName = source["assetName"];
	        this.assetSize = source["assetSize"];
	        this.sha256Url = source["sha256Url"];
	    }
	}

}

