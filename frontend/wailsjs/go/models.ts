export namespace executor {
	
	export class SkillResult {
	    updatedFiles: session.WorkingFile[];
	    session: session.SessionSnapshot;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new SkillResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updatedFiles = this.convertValues(source["updatedFiles"], session.WorkingFile);
	        this.session = this.convertValues(source["session"], session.SessionSnapshot);
	        this.message = source["message"];
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

export namespace session {
	
	export class AppliedSkill {
	    skillId: string;
	    params: Record<string, any>;
	    appliedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new AppliedSkill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.skillId = source["skillId"];
	        this.params = source["params"];
	        this.appliedAt = source["appliedAt"];
	    }
	}
	export class ExportResult {
	    fileId: string;
	    outputPath: string;
	
	    static createFrom(source: any = {}) {
	        return new ExportResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileId = source["fileId"];
	        this.outputPath = source["outputPath"];
	    }
	}
	export class SessionSnapshot {
	    mode: string;
	    outputFolder: string;
	    namingPattern: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.outputFolder = source["outputFolder"];
	        this.namingPattern = source["namingPattern"];
	    }
	}
	export class WorkingFile {
	    id: string;
	    name: string;
	    extension: string;
	    currentExtension: string;
	    originalPath: string;
	    workingPath: string;
	    size: number;
	    previewDataUrl: string;
	    appliedSkills: AppliedSkill[];
	
	    static createFrom(source: any = {}) {
	        return new WorkingFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.extension = source["extension"];
	        this.currentExtension = source["currentExtension"];
	        this.originalPath = source["originalPath"];
	        this.workingPath = source["workingPath"];
	        this.size = source["size"];
	        this.previewDataUrl = source["previewDataUrl"];
	        this.appliedSkills = this.convertValues(source["appliedSkills"], AppliedSkill);
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
	
	export class ParamDef {
	    name: string;
	    type: string;
	    label: string;
	    default: any;
	    presets?: any[];
	    options?: string[];
	    min?: number;
	    max?: number;
	    unit?: string;
	
	    static createFrom(source: any = {}) {
	        return new ParamDef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.label = source["label"];
	        this.default = source["default"];
	        this.presets = source["presets"];
	        this.options = source["options"];
	        this.min = source["min"];
	        this.max = source["max"];
	        this.unit = source["unit"];
	    }
	}
	export class Skill {
	    id: string;
	    name: string;
	    aliases: string[];
	    category: string;
	    description: string;
	    inputTypes: string[];
	    outputType: string;
	    params: ParamDef[];
	    driver: string;
	    isMeta: boolean;
	    dangerLevel: number;
	
	    static createFrom(source: any = {}) {
	        return new Skill(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.aliases = source["aliases"];
	        this.category = source["category"];
	        this.description = source["description"];
	        this.inputTypes = source["inputTypes"];
	        this.outputType = source["outputType"];
	        this.params = this.convertValues(source["params"], ParamDef);
	        this.driver = source["driver"];
	        this.isMeta = source["isMeta"];
	        this.dangerLevel = source["dangerLevel"];
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

