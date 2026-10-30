export namespace logscan {
	
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
	export class QueryStats {
	    total: number;
	    truncated: boolean;
	    costMs: number;
	    levelCounts: number[];
	    maxLineLen: number;
	
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

}

export namespace main {
	
	export class AddFilesResult {
	    files: logscan.FileEntry[];
	    errors: string[];
	    allFiles: logscan.FileEntry[];
	
	    static createFrom(source: any = {}) {
	        return new AddFilesResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.files = this.convertValues(source["files"], logscan.FileEntry);
	        this.errors = source["errors"];
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

}

