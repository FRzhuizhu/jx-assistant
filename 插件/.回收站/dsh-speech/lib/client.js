window.__ModuleLoader__.load({ id: "dsh-speech", factory: (require) => {
var module = { exports: {} }; var exports = module.exports;
Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
let react = require("react");
let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
let react_jsx_runtime = require("react/jsx-runtime");
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/core.js
var _a$1;
function $constructor(name, initializer, params) {
	function init(inst, def) {
		if (!inst._zod) Object.defineProperty(inst, "_zod", {
			value: {
				def,
				constr: _,
				traits: /* @__PURE__ */ new Set()
			},
			enumerable: false
		});
		if (inst._zod.traits.has(name)) return;
		inst._zod.traits.add(name);
		initializer(inst, def);
		const proto = _.prototype;
		const keys = Object.keys(proto);
		for (let i = 0; i < keys.length; i++) {
			const k = keys[i];
			if (!(k in inst)) inst[k] = proto[k].bind(inst);
		}
	}
	const Parent = params?.Parent ?? Object;
	class Definition extends Parent {}
	Object.defineProperty(Definition, "name", { value: name });
	function _(def) {
		var _a;
		const inst = params?.Parent ? new Definition() : this;
		init(inst, def);
		(_a = inst._zod).deferred ?? (_a.deferred = []);
		for (const fn of inst._zod.deferred) fn();
		return inst;
	}
	Object.defineProperty(_, "init", { value: init });
	Object.defineProperty(_, Symbol.hasInstance, { value: (inst) => {
		if (params?.Parent && inst instanceof params.Parent) return true;
		return inst?._zod?.traits?.has(name);
	} });
	Object.defineProperty(_, "name", { value: name });
	return _;
}
var $ZodAsyncError = class extends Error {
	constructor() {
		super(`Encountered Promise during synchronous parse. Use .parseAsync() instead.`);
	}
};
var $ZodEncodeError = class extends Error {
	constructor(name) {
		super(`Encountered unidirectional transform during encode: ${name}`);
		this.name = "ZodEncodeError";
	}
};
(_a$1 = globalThis).__zod_globalConfig ?? (_a$1.__zod_globalConfig = {});
const globalConfig = globalThis.__zod_globalConfig;
function config(newConfig) {
	if (newConfig) Object.assign(globalConfig, newConfig);
	return globalConfig;
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/util.js
function getEnumValues(entries) {
	const numericValues = Object.values(entries).filter((v) => typeof v === "number");
	return Object.entries(entries).filter(([k, _]) => numericValues.indexOf(+k) === -1).map(([_, v]) => v);
}
function jsonStringifyReplacer(_, value) {
	if (typeof value === "bigint") return value.toString();
	return value;
}
function cached(getter) {
	return { get value() {
		{
			const value = getter();
			Object.defineProperty(this, "value", { value });
			return value;
		}
		throw new Error("cached value already set");
	} };
}
function nullish(input) {
	return input === null || input === void 0;
}
function cleanRegex(source) {
	const start = source.startsWith("^") ? 1 : 0;
	const end = source.endsWith("$") ? source.length - 1 : source.length;
	return source.slice(start, end);
}
function floatSafeRemainder(val, step) {
	const ratio = val / step;
	const roundedRatio = Math.round(ratio);
	const tolerance = Number.EPSILON * Math.max(Math.abs(ratio), 1);
	if (Math.abs(ratio - roundedRatio) < tolerance) return 0;
	return ratio - roundedRatio;
}
const EVALUATING = /* @__PURE__*/ Symbol("evaluating");
function defineLazy(object, key, getter) {
	let value = void 0;
	Object.defineProperty(object, key, {
		get() {
			if (value === EVALUATING) return;
			if (value === void 0) {
				value = EVALUATING;
				value = getter();
			}
			return value;
		},
		set(v) {
			Object.defineProperty(object, key, { value: v });
		},
		configurable: true
	});
}
function assignProp(target, prop, value) {
	Object.defineProperty(target, prop, {
		value,
		writable: true,
		enumerable: true,
		configurable: true
	});
}
function mergeDefs(...defs) {
	const mergedDescriptors = {};
	for (const def of defs) Object.assign(mergedDescriptors, Object.getOwnPropertyDescriptors(def));
	return Object.defineProperties({}, mergedDescriptors);
}
function esc(str) {
	return JSON.stringify(str);
}
function slugify(input) {
	return input.toLowerCase().trim().replace(/[^\w\s-]/g, "").replace(/[\s_-]+/g, "-").replace(/^-+|-+$/g, "");
}
const captureStackTrace = "captureStackTrace" in Error ? Error.captureStackTrace : (..._args) => {};
function isObject(data) {
	return typeof data === "object" && data !== null && !Array.isArray(data);
}
const allowsEval = /* @__PURE__*/ cached(() => {
	if (globalConfig.jitless) return false;
	if (typeof navigator !== "undefined" && navigator?.userAgent?.includes("Cloudflare")) return false;
	try {
		new Function("");
		return true;
	} catch (_) {
		return false;
	}
});
function isPlainObject(o) {
	if (isObject(o) === false) return false;
	const ctor = o.constructor;
	if (ctor === void 0) return true;
	if (typeof ctor !== "function") return true;
	const prot = ctor.prototype;
	if (isObject(prot) === false) return false;
	if (Object.prototype.hasOwnProperty.call(prot, "isPrototypeOf") === false) return false;
	return true;
}
function shallowClone(o) {
	if (isPlainObject(o)) return { ...o };
	if (Array.isArray(o)) return [...o];
	if (o instanceof Map) return new Map(o);
	if (o instanceof Set) return new Set(o);
	return o;
}
const propertyKeyTypes = /* @__PURE__*/ new Set([
	"string",
	"number",
	"symbol"
]);
function escapeRegex(str) {
	return str.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function clone(inst, def, params) {
	const cl = new inst._zod.constr(def ?? inst._zod.def);
	if (!def || params?.parent) cl._zod.parent = inst;
	return cl;
}
function normalizeParams(_params) {
	const params = _params;
	if (!params) return {};
	if (typeof params === "string") return { error: () => params };
	if (params?.message !== void 0) {
		if (params?.error !== void 0) throw new Error("Cannot specify both `message` and `error` params");
		params.error = params.message;
	}
	delete params.message;
	if (typeof params.error === "string") return {
		...params,
		error: () => params.error
	};
	return params;
}
function optionalKeys(shape) {
	return Object.keys(shape).filter((k) => {
		return shape[k]._zod.optin === "optional" && shape[k]._zod.optout === "optional";
	});
}
const NUMBER_FORMAT_RANGES = {
	safeint: [Number.MIN_SAFE_INTEGER, Number.MAX_SAFE_INTEGER],
	int32: [-2147483648, 2147483647],
	uint32: [0, 4294967295],
	float32: [-34028234663852886e22, 34028234663852886e22],
	float64: [-Number.MAX_VALUE, Number.MAX_VALUE]
};
function pick(schema, mask) {
	const currDef = schema._zod.def;
	const checks = currDef.checks;
	if (checks && checks.length > 0) throw new Error(".pick() cannot be used on object schemas containing refinements");
	return clone(schema, mergeDefs(schema._zod.def, {
		get shape() {
			const newShape = {};
			for (const key in mask) {
				if (!(key in currDef.shape)) throw new Error(`Unrecognized key: "${key}"`);
				if (!mask[key]) continue;
				newShape[key] = currDef.shape[key];
			}
			assignProp(this, "shape", newShape);
			return newShape;
		},
		checks: []
	}));
}
function omit(schema, mask) {
	const currDef = schema._zod.def;
	const checks = currDef.checks;
	if (checks && checks.length > 0) throw new Error(".omit() cannot be used on object schemas containing refinements");
	return clone(schema, mergeDefs(schema._zod.def, {
		get shape() {
			const newShape = { ...schema._zod.def.shape };
			for (const key in mask) {
				if (!(key in currDef.shape)) throw new Error(`Unrecognized key: "${key}"`);
				if (!mask[key]) continue;
				delete newShape[key];
			}
			assignProp(this, "shape", newShape);
			return newShape;
		},
		checks: []
	}));
}
function extend(schema, shape) {
	if (!isPlainObject(shape)) throw new Error("Invalid input to extend: expected a plain object");
	const checks = schema._zod.def.checks;
	if (checks && checks.length > 0) {
		const existingShape = schema._zod.def.shape;
		for (const key in shape) if (Object.getOwnPropertyDescriptor(existingShape, key) !== void 0) throw new Error("Cannot overwrite keys on object schemas containing refinements. Use `.safeExtend()` instead.");
	}
	return clone(schema, mergeDefs(schema._zod.def, { get shape() {
		const _shape = {
			...schema._zod.def.shape,
			...shape
		};
		assignProp(this, "shape", _shape);
		return _shape;
	} }));
}
function safeExtend(schema, shape) {
	if (!isPlainObject(shape)) throw new Error("Invalid input to safeExtend: expected a plain object");
	return clone(schema, mergeDefs(schema._zod.def, { get shape() {
		const _shape = {
			...schema._zod.def.shape,
			...shape
		};
		assignProp(this, "shape", _shape);
		return _shape;
	} }));
}
function merge(a, b) {
	if (a._zod.def.checks?.length) throw new Error(".merge() cannot be used on object schemas containing refinements. Use .safeExtend() instead.");
	return clone(a, mergeDefs(a._zod.def, {
		get shape() {
			const _shape = {
				...a._zod.def.shape,
				...b._zod.def.shape
			};
			assignProp(this, "shape", _shape);
			return _shape;
		},
		get catchall() {
			return b._zod.def.catchall;
		},
		checks: b._zod.def.checks ?? []
	}));
}
function partial(Class, schema, mask) {
	const checks = schema._zod.def.checks;
	if (checks && checks.length > 0) throw new Error(".partial() cannot be used on object schemas containing refinements");
	return clone(schema, mergeDefs(schema._zod.def, {
		get shape() {
			const oldShape = schema._zod.def.shape;
			const shape = { ...oldShape };
			if (mask) for (const key in mask) {
				if (!(key in oldShape)) throw new Error(`Unrecognized key: "${key}"`);
				if (!mask[key]) continue;
				shape[key] = Class ? new Class({
					type: "optional",
					innerType: oldShape[key]
				}) : oldShape[key];
			}
			else for (const key in oldShape) shape[key] = Class ? new Class({
				type: "optional",
				innerType: oldShape[key]
			}) : oldShape[key];
			assignProp(this, "shape", shape);
			return shape;
		},
		checks: []
	}));
}
function required(Class, schema, mask) {
	return clone(schema, mergeDefs(schema._zod.def, { get shape() {
		const oldShape = schema._zod.def.shape;
		const shape = { ...oldShape };
		if (mask) for (const key in mask) {
			if (!(key in shape)) throw new Error(`Unrecognized key: "${key}"`);
			if (!mask[key]) continue;
			shape[key] = new Class({
				type: "nonoptional",
				innerType: oldShape[key]
			});
		}
		else for (const key in oldShape) shape[key] = new Class({
			type: "nonoptional",
			innerType: oldShape[key]
		});
		assignProp(this, "shape", shape);
		return shape;
	} }));
}
function aborted(x, startIndex = 0) {
	if (x.aborted === true) return true;
	for (let i = startIndex; i < x.issues.length; i++) if (x.issues[i]?.continue !== true) return true;
	return false;
}
function explicitlyAborted(x, startIndex = 0) {
	if (x.aborted === true) return true;
	for (let i = startIndex; i < x.issues.length; i++) if (x.issues[i]?.continue === false) return true;
	return false;
}
function prefixIssues(path, issues) {
	return issues.map((iss) => {
		var _a;
		(_a = iss).path ?? (_a.path = []);
		iss.path.unshift(path);
		return iss;
	});
}
function unwrapMessage(message) {
	return typeof message === "string" ? message : message?.message;
}
function finalizeIssue(iss, ctx, config) {
	const message = iss.message ? iss.message : unwrapMessage(iss.inst?._zod.def?.error?.(iss)) ?? unwrapMessage(ctx?.error?.(iss)) ?? unwrapMessage(config.customError?.(iss)) ?? unwrapMessage(config.localeError?.(iss)) ?? "Invalid input";
	const { inst: _inst, continue: _continue, input: _input, ...rest } = iss;
	rest.path ?? (rest.path = []);
	rest.message = message;
	if (ctx?.reportInput) rest.input = _input;
	return rest;
}
function getLengthableOrigin(input) {
	if (Array.isArray(input)) return "array";
	if (typeof input === "string") return "string";
	return "unknown";
}
function issue(...args) {
	const [iss, input, inst] = args;
	if (typeof iss === "string") return {
		message: iss,
		code: "custom",
		input,
		inst
	};
	return { ...iss };
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/errors.js
const initializer$1 = (inst, def) => {
	inst.name = "$ZodError";
	Object.defineProperty(inst, "_zod", {
		value: inst._zod,
		enumerable: false
	});
	Object.defineProperty(inst, "issues", {
		value: def,
		enumerable: false
	});
	inst.message = JSON.stringify(def, jsonStringifyReplacer, 2);
	Object.defineProperty(inst, "toString", {
		value: () => inst.message,
		enumerable: false
	});
};
const $ZodError = $constructor("$ZodError", initializer$1);
const $ZodRealError = $constructor("$ZodError", initializer$1, { Parent: Error });
function flattenError(error, mapper = (issue) => issue.message) {
	const fieldErrors = {};
	const formErrors = [];
	for (const sub of error.issues) if (sub.path.length > 0) {
		fieldErrors[sub.path[0]] = fieldErrors[sub.path[0]] || [];
		fieldErrors[sub.path[0]].push(mapper(sub));
	} else formErrors.push(mapper(sub));
	return {
		formErrors,
		fieldErrors
	};
}
function formatError(error, mapper = (issue) => issue.message) {
	const fieldErrors = { _errors: [] };
	const processError = (error, path = []) => {
		for (const issue of error.issues) if (issue.code === "invalid_union" && issue.errors.length) issue.errors.map((issues) => processError({ issues }, [...path, ...issue.path]));
		else if (issue.code === "invalid_key") processError({ issues: issue.issues }, [...path, ...issue.path]);
		else if (issue.code === "invalid_element") processError({ issues: issue.issues }, [...path, ...issue.path]);
		else {
			const fullpath = [...path, ...issue.path];
			if (fullpath.length === 0) fieldErrors._errors.push(mapper(issue));
			else {
				let curr = fieldErrors;
				let i = 0;
				while (i < fullpath.length) {
					const el = fullpath[i];
					if (!(i === fullpath.length - 1)) curr[el] = curr[el] || { _errors: [] };
					else {
						curr[el] = curr[el] || { _errors: [] };
						curr[el]._errors.push(mapper(issue));
					}
					curr = curr[el];
					i++;
				}
			}
		}
	};
	processError(error);
	return fieldErrors;
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/parse.js
const _parse = (_Err) => (schema, value, _ctx, _params) => {
	const ctx = _ctx ? {
		..._ctx,
		async: false
	} : { async: false };
	const result = schema._zod.run({
		value,
		issues: []
	}, ctx);
	if (result instanceof Promise) throw new $ZodAsyncError();
	if (result.issues.length) {
		const e = new ((_params?.Err) ?? _Err)(result.issues.map((iss) => finalizeIssue(iss, ctx, config())));
		captureStackTrace(e, _params?.callee);
		throw e;
	}
	return result.value;
};
const _parseAsync = (_Err) => async (schema, value, _ctx, params) => {
	const ctx = _ctx ? {
		..._ctx,
		async: true
	} : { async: true };
	let result = schema._zod.run({
		value,
		issues: []
	}, ctx);
	if (result instanceof Promise) result = await result;
	if (result.issues.length) {
		const e = new ((params?.Err) ?? _Err)(result.issues.map((iss) => finalizeIssue(iss, ctx, config())));
		captureStackTrace(e, params?.callee);
		throw e;
	}
	return result.value;
};
const _safeParse = (_Err) => (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		async: false
	} : { async: false };
	const result = schema._zod.run({
		value,
		issues: []
	}, ctx);
	if (result instanceof Promise) throw new $ZodAsyncError();
	return result.issues.length ? {
		success: false,
		error: new (_Err ?? $ZodError)(result.issues.map((iss) => finalizeIssue(iss, ctx, config())))
	} : {
		success: true,
		data: result.value
	};
};
const safeParse$1 = /* @__PURE__*/ _safeParse($ZodRealError);
const _safeParseAsync = (_Err) => async (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		async: true
	} : { async: true };
	let result = schema._zod.run({
		value,
		issues: []
	}, ctx);
	if (result instanceof Promise) result = await result;
	return result.issues.length ? {
		success: false,
		error: new _Err(result.issues.map((iss) => finalizeIssue(iss, ctx, config())))
	} : {
		success: true,
		data: result.value
	};
};
const safeParseAsync$1 = /* @__PURE__*/ _safeParseAsync($ZodRealError);
const _encode = (_Err) => (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		direction: "backward"
	} : { direction: "backward" };
	return _parse(_Err)(schema, value, ctx);
};
const _decode = (_Err) => (schema, value, _ctx) => {
	return _parse(_Err)(schema, value, _ctx);
};
const _encodeAsync = (_Err) => async (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		direction: "backward"
	} : { direction: "backward" };
	return _parseAsync(_Err)(schema, value, ctx);
};
const _decodeAsync = (_Err) => async (schema, value, _ctx) => {
	return _parseAsync(_Err)(schema, value, _ctx);
};
const _safeEncode = (_Err) => (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		direction: "backward"
	} : { direction: "backward" };
	return _safeParse(_Err)(schema, value, ctx);
};
const _safeDecode = (_Err) => (schema, value, _ctx) => {
	return _safeParse(_Err)(schema, value, _ctx);
};
const _safeEncodeAsync = (_Err) => async (schema, value, _ctx) => {
	const ctx = _ctx ? {
		..._ctx,
		direction: "backward"
	} : { direction: "backward" };
	return _safeParseAsync(_Err)(schema, value, ctx);
};
const _safeDecodeAsync = (_Err) => async (schema, value, _ctx) => {
	return _safeParseAsync(_Err)(schema, value, _ctx);
};
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/regexes.js
/**
* @deprecated CUID v1 is deprecated by its authors due to information leakage
* (timestamps embedded in the id). Use {@link cuid2} instead.
* See https://github.com/paralleldrive/cuid.
*/
const cuid = /^[cC][0-9a-z]{6,}$/;
const cuid2 = /^[0-9a-z]+$/;
const ulid = /^[0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}$/;
const xid = /^[0-9a-vA-V]{20}$/;
const ksuid = /^[A-Za-z0-9]{27}$/;
const nanoid = /^[a-zA-Z0-9_-]{21}$/;
/** ISO 8601-1 duration regex. Does not support the 8601-2 extensions like negative durations or fractional/negative components. */
const duration$1 = /^P(?:(\d+W)|(?!.*W)(?=\d|T\d)(\d+Y)?(\d+M)?(\d+D)?(T(?=\d)(\d+H)?(\d+M)?(\d+([.,]\d+)?S)?)?)$/;
/** A regex for any UUID-like identifier: 8-4-4-4-12 hex pattern */
const guid = /^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$/;
/** Returns a regex for validating an RFC 9562/4122 UUID.
*
* @param version Optionally specify a version 1-8. If no version is specified, all versions are supported. */
const uuid = (version) => {
	if (!version) return /^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$/;
	return new RegExp(`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-${version}[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})$`);
};
/** Practical email validation */
const email = /^(?!\.)(?!.*\.\.)([A-Za-z0-9_'+\-\.]*)[A-Za-z0-9_+-]@([A-Za-z0-9][A-Za-z0-9\-]*\.)+[A-Za-z]{2,}$/;
const _emoji$1 = `^(\\p{Extended_Pictographic}|\\p{Emoji_Component})+$`;
function emoji() {
	return new RegExp(_emoji$1, "u");
}
const ipv4 = /^(?:(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9][0-9]|[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9][0-9]|[0-9])$/;
const ipv6 = /^(([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:))$/;
const cidrv4 = /^((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9][0-9]|[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9][0-9]|[0-9])\/([0-9]|[1-2][0-9]|3[0-2])$/;
const cidrv6 = /^(([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|::|([0-9a-fA-F]{1,4})?::([0-9a-fA-F]{1,4}:?){0,6})\/(12[0-8]|1[01][0-9]|[1-9]?[0-9])$/;
const base64 = /^$|^(?:[0-9a-zA-Z+/]{4})*(?:(?:[0-9a-zA-Z+/]{2}==)|(?:[0-9a-zA-Z+/]{3}=))?$/;
const base64url = /^[A-Za-z0-9_-]*$/;
const httpProtocol = /^https?$/;
const e164 = /^\+[1-9]\d{6,14}$/;
const dateSource = `(?:(?:\\d\\d[2468][048]|\\d\\d[13579][26]|\\d\\d0[48]|[02468][048]00|[13579][26]00)-02-29|\\d{4}-(?:(?:0[13578]|1[02])-(?:0[1-9]|[12]\\d|3[01])|(?:0[469]|11)-(?:0[1-9]|[12]\\d|30)|(?:02)-(?:0[1-9]|1\\d|2[0-8])))`;
const date$1 = /*@__PURE__*/ new RegExp(`^${dateSource}$`);
function timeSource(args) {
	const hhmm = `(?:[01]\\d|2[0-3]):[0-5]\\d`;
	return typeof args.precision === "number" ? args.precision === -1 ? `${hhmm}` : args.precision === 0 ? `${hhmm}:[0-5]\\d` : `${hhmm}:[0-5]\\d\\.\\d{${args.precision}}` : `${hhmm}(?::[0-5]\\d(?:\\.\\d+)?)?`;
}
function time$1(args) {
	return new RegExp(`^${timeSource(args)}$`);
}
function datetime$1(args) {
	const time = timeSource({ precision: args.precision });
	const opts = ["Z"];
	if (args.local) opts.push("");
	if (args.offset) opts.push(`([+-](?:[01]\\d|2[0-3]):[0-5]\\d)`);
	const timeRegex = `${time}(?:${opts.join("|")})`;
	return new RegExp(`^${dateSource}T(?:${timeRegex})$`);
}
const string$1 = (params) => {
	const regex = params ? `[\\s\\S]{${params?.minimum ?? 0},${params?.maximum ?? ""}}` : `[\\s\\S]*`;
	return new RegExp(`^${regex}$`);
};
const integer = /^-?\d+$/;
const number$1 = /^-?\d+(?:\.\d+)?$/;
const boolean$1 = /^(?:true|false)$/i;
const lowercase = /^[^A-Z]*$/;
const uppercase = /^[^a-z]*$/;
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/checks.js
const $ZodCheck = /*@__PURE__*/ $constructor("$ZodCheck", (inst, def) => {
	var _a;
	inst._zod ?? (inst._zod = {});
	inst._zod.def = def;
	(_a = inst._zod).onattach ?? (_a.onattach = []);
});
const numericOriginMap = {
	number: "number",
	bigint: "bigint",
	object: "date"
};
const $ZodCheckLessThan = /*@__PURE__*/ $constructor("$ZodCheckLessThan", (inst, def) => {
	$ZodCheck.init(inst, def);
	const origin = numericOriginMap[typeof def.value];
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		const curr = (def.inclusive ? bag.maximum : bag.exclusiveMaximum) ?? Number.POSITIVE_INFINITY;
		if (def.value < curr) if (def.inclusive) bag.maximum = def.value;
		else bag.exclusiveMaximum = def.value;
	});
	inst._zod.check = (payload) => {
		if (def.inclusive ? payload.value <= def.value : payload.value < def.value) return;
		payload.issues.push({
			origin,
			code: "too_big",
			maximum: typeof def.value === "object" ? def.value.getTime() : def.value,
			input: payload.value,
			inclusive: def.inclusive,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckGreaterThan = /*@__PURE__*/ $constructor("$ZodCheckGreaterThan", (inst, def) => {
	$ZodCheck.init(inst, def);
	const origin = numericOriginMap[typeof def.value];
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		const curr = (def.inclusive ? bag.minimum : bag.exclusiveMinimum) ?? Number.NEGATIVE_INFINITY;
		if (def.value > curr) if (def.inclusive) bag.minimum = def.value;
		else bag.exclusiveMinimum = def.value;
	});
	inst._zod.check = (payload) => {
		if (def.inclusive ? payload.value >= def.value : payload.value > def.value) return;
		payload.issues.push({
			origin,
			code: "too_small",
			minimum: typeof def.value === "object" ? def.value.getTime() : def.value,
			input: payload.value,
			inclusive: def.inclusive,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckMultipleOf = /*@__PURE__*/ $constructor("$ZodCheckMultipleOf", (inst, def) => {
	$ZodCheck.init(inst, def);
	inst._zod.onattach.push((inst) => {
		var _a;
		(_a = inst._zod.bag).multipleOf ?? (_a.multipleOf = def.value);
	});
	inst._zod.check = (payload) => {
		if (typeof payload.value !== typeof def.value) throw new Error("Cannot mix number and bigint in multiple_of check.");
		if (typeof payload.value === "bigint" ? payload.value % def.value === BigInt(0) : floatSafeRemainder(payload.value, def.value) === 0) return;
		payload.issues.push({
			origin: typeof payload.value,
			code: "not_multiple_of",
			divisor: def.value,
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckNumberFormat = /*@__PURE__*/ $constructor("$ZodCheckNumberFormat", (inst, def) => {
	$ZodCheck.init(inst, def);
	def.format = def.format || "float64";
	const isInt = def.format?.includes("int");
	const origin = isInt ? "int" : "number";
	const [minimum, maximum] = NUMBER_FORMAT_RANGES[def.format];
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.format = def.format;
		bag.minimum = minimum;
		bag.maximum = maximum;
		if (isInt) bag.pattern = integer;
	});
	inst._zod.check = (payload) => {
		const input = payload.value;
		if (isInt) {
			if (!Number.isInteger(input)) {
				payload.issues.push({
					expected: origin,
					format: def.format,
					code: "invalid_type",
					continue: false,
					input,
					inst
				});
				return;
			}
			if (!Number.isSafeInteger(input)) {
				if (input > 0) payload.issues.push({
					input,
					code: "too_big",
					maximum: Number.MAX_SAFE_INTEGER,
					note: "Integers must be within the safe integer range.",
					inst,
					origin,
					inclusive: true,
					continue: !def.abort
				});
				else payload.issues.push({
					input,
					code: "too_small",
					minimum: Number.MIN_SAFE_INTEGER,
					note: "Integers must be within the safe integer range.",
					inst,
					origin,
					inclusive: true,
					continue: !def.abort
				});
				return;
			}
		}
		if (input < minimum) payload.issues.push({
			origin: "number",
			input,
			code: "too_small",
			minimum,
			inclusive: true,
			inst,
			continue: !def.abort
		});
		if (input > maximum) payload.issues.push({
			origin: "number",
			input,
			code: "too_big",
			maximum,
			inclusive: true,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckMaxLength = /*@__PURE__*/ $constructor("$ZodCheckMaxLength", (inst, def) => {
	var _a;
	$ZodCheck.init(inst, def);
	(_a = inst._zod.def).when ?? (_a.when = (payload) => {
		const val = payload.value;
		return !nullish(val) && val.length !== void 0;
	});
	inst._zod.onattach.push((inst) => {
		const curr = inst._zod.bag.maximum ?? Number.POSITIVE_INFINITY;
		if (def.maximum < curr) inst._zod.bag.maximum = def.maximum;
	});
	inst._zod.check = (payload) => {
		const input = payload.value;
		if (input.length <= def.maximum) return;
		const origin = getLengthableOrigin(input);
		payload.issues.push({
			origin,
			code: "too_big",
			maximum: def.maximum,
			inclusive: true,
			input,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckMinLength = /*@__PURE__*/ $constructor("$ZodCheckMinLength", (inst, def) => {
	var _a;
	$ZodCheck.init(inst, def);
	(_a = inst._zod.def).when ?? (_a.when = (payload) => {
		const val = payload.value;
		return !nullish(val) && val.length !== void 0;
	});
	inst._zod.onattach.push((inst) => {
		const curr = inst._zod.bag.minimum ?? Number.NEGATIVE_INFINITY;
		if (def.minimum > curr) inst._zod.bag.minimum = def.minimum;
	});
	inst._zod.check = (payload) => {
		const input = payload.value;
		if (input.length >= def.minimum) return;
		const origin = getLengthableOrigin(input);
		payload.issues.push({
			origin,
			code: "too_small",
			minimum: def.minimum,
			inclusive: true,
			input,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckLengthEquals = /*@__PURE__*/ $constructor("$ZodCheckLengthEquals", (inst, def) => {
	var _a;
	$ZodCheck.init(inst, def);
	(_a = inst._zod.def).when ?? (_a.when = (payload) => {
		const val = payload.value;
		return !nullish(val) && val.length !== void 0;
	});
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.minimum = def.length;
		bag.maximum = def.length;
		bag.length = def.length;
	});
	inst._zod.check = (payload) => {
		const input = payload.value;
		const length = input.length;
		if (length === def.length) return;
		const origin = getLengthableOrigin(input);
		const tooBig = length > def.length;
		payload.issues.push({
			origin,
			...tooBig ? {
				code: "too_big",
				maximum: def.length
			} : {
				code: "too_small",
				minimum: def.length
			},
			inclusive: true,
			exact: true,
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckStringFormat = /*@__PURE__*/ $constructor("$ZodCheckStringFormat", (inst, def) => {
	var _a, _b;
	$ZodCheck.init(inst, def);
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.format = def.format;
		if (def.pattern) {
			bag.patterns ?? (bag.patterns = /* @__PURE__ */ new Set());
			bag.patterns.add(def.pattern);
		}
	});
	if (def.pattern) (_a = inst._zod).check ?? (_a.check = (payload) => {
		def.pattern.lastIndex = 0;
		if (def.pattern.test(payload.value)) return;
		payload.issues.push({
			origin: "string",
			code: "invalid_format",
			format: def.format,
			input: payload.value,
			...def.pattern ? { pattern: def.pattern.toString() } : {},
			inst,
			continue: !def.abort
		});
	});
	else (_b = inst._zod).check ?? (_b.check = () => {});
});
const $ZodCheckRegex = /*@__PURE__*/ $constructor("$ZodCheckRegex", (inst, def) => {
	$ZodCheckStringFormat.init(inst, def);
	inst._zod.check = (payload) => {
		def.pattern.lastIndex = 0;
		if (def.pattern.test(payload.value)) return;
		payload.issues.push({
			origin: "string",
			code: "invalid_format",
			format: "regex",
			input: payload.value,
			pattern: def.pattern.toString(),
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckLowerCase = /*@__PURE__*/ $constructor("$ZodCheckLowerCase", (inst, def) => {
	def.pattern ?? (def.pattern = lowercase);
	$ZodCheckStringFormat.init(inst, def);
});
const $ZodCheckUpperCase = /*@__PURE__*/ $constructor("$ZodCheckUpperCase", (inst, def) => {
	def.pattern ?? (def.pattern = uppercase);
	$ZodCheckStringFormat.init(inst, def);
});
const $ZodCheckIncludes = /*@__PURE__*/ $constructor("$ZodCheckIncludes", (inst, def) => {
	$ZodCheck.init(inst, def);
	const escapedRegex = escapeRegex(def.includes);
	const pattern = new RegExp(typeof def.position === "number" ? `^.{${def.position}}${escapedRegex}` : escapedRegex);
	def.pattern = pattern;
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.patterns ?? (bag.patterns = /* @__PURE__ */ new Set());
		bag.patterns.add(pattern);
	});
	inst._zod.check = (payload) => {
		if (payload.value.includes(def.includes, def.position)) return;
		payload.issues.push({
			origin: "string",
			code: "invalid_format",
			format: "includes",
			includes: def.includes,
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckStartsWith = /*@__PURE__*/ $constructor("$ZodCheckStartsWith", (inst, def) => {
	$ZodCheck.init(inst, def);
	const pattern = new RegExp(`^${escapeRegex(def.prefix)}.*`);
	def.pattern ?? (def.pattern = pattern);
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.patterns ?? (bag.patterns = /* @__PURE__ */ new Set());
		bag.patterns.add(pattern);
	});
	inst._zod.check = (payload) => {
		if (payload.value.startsWith(def.prefix)) return;
		payload.issues.push({
			origin: "string",
			code: "invalid_format",
			format: "starts_with",
			prefix: def.prefix,
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckEndsWith = /*@__PURE__*/ $constructor("$ZodCheckEndsWith", (inst, def) => {
	$ZodCheck.init(inst, def);
	const pattern = new RegExp(`.*${escapeRegex(def.suffix)}$`);
	def.pattern ?? (def.pattern = pattern);
	inst._zod.onattach.push((inst) => {
		const bag = inst._zod.bag;
		bag.patterns ?? (bag.patterns = /* @__PURE__ */ new Set());
		bag.patterns.add(pattern);
	});
	inst._zod.check = (payload) => {
		if (payload.value.endsWith(def.suffix)) return;
		payload.issues.push({
			origin: "string",
			code: "invalid_format",
			format: "ends_with",
			suffix: def.suffix,
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodCheckOverwrite = /*@__PURE__*/ $constructor("$ZodCheckOverwrite", (inst, def) => {
	$ZodCheck.init(inst, def);
	inst._zod.check = (payload) => {
		payload.value = def.tx(payload.value);
	};
});
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/doc.js
var Doc = class {
	constructor(args = []) {
		this.content = [];
		this.indent = 0;
		if (this) this.args = args;
	}
	indented(fn) {
		this.indent += 1;
		fn(this);
		this.indent -= 1;
	}
	write(arg) {
		if (typeof arg === "function") {
			arg(this, { execution: "sync" });
			arg(this, { execution: "async" });
			return;
		}
		const lines = arg.split("\n").filter((x) => x);
		const minIndent = Math.min(...lines.map((x) => x.length - x.trimStart().length));
		const dedented = lines.map((x) => x.slice(minIndent)).map((x) => " ".repeat(this.indent * 2) + x);
		for (const line of dedented) this.content.push(line);
	}
	compile() {
		const F = Function;
		const args = this?.args;
		const lines = [...(this?.content ?? [``]).map((x) => `  ${x}`)];
		return new F(...args, lines.join("\n"));
	}
};
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/versions.js
const version = {
	major: 4,
	minor: 4,
	patch: 3
};
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/schemas.js
const $ZodType = /*@__PURE__*/ $constructor("$ZodType", (inst, def) => {
	var _a;
	inst ?? (inst = {});
	inst._zod.def = def;
	inst._zod.bag = inst._zod.bag || {};
	inst._zod.version = version;
	const checks = [...inst._zod.def.checks ?? []];
	if (inst._zod.traits.has("$ZodCheck")) checks.unshift(inst);
	for (const ch of checks) for (const fn of ch._zod.onattach) fn(inst);
	if (checks.length === 0) {
		(_a = inst._zod).deferred ?? (_a.deferred = []);
		inst._zod.deferred?.push(() => {
			inst._zod.run = inst._zod.parse;
		});
	} else {
		const runChecks = (payload, checks, ctx) => {
			let isAborted = aborted(payload);
			let asyncResult;
			for (const ch of checks) {
				if (ch._zod.def.when) {
					if (explicitlyAborted(payload)) continue;
					if (!ch._zod.def.when(payload)) continue;
				} else if (isAborted) continue;
				const currLen = payload.issues.length;
				const _ = ch._zod.check(payload);
				if (_ instanceof Promise && ctx?.async === false) throw new $ZodAsyncError();
				if (asyncResult || _ instanceof Promise) asyncResult = (asyncResult ?? Promise.resolve()).then(async () => {
					await _;
					if (payload.issues.length === currLen) return;
					if (!isAborted) isAborted = aborted(payload, currLen);
				});
				else {
					if (payload.issues.length === currLen) continue;
					if (!isAborted) isAborted = aborted(payload, currLen);
				}
			}
			if (asyncResult) return asyncResult.then(() => {
				return payload;
			});
			return payload;
		};
		const handleCanaryResult = (canary, payload, ctx) => {
			if (aborted(canary)) {
				canary.aborted = true;
				return canary;
			}
			const checkResult = runChecks(payload, checks, ctx);
			if (checkResult instanceof Promise) {
				if (ctx.async === false) throw new $ZodAsyncError();
				return checkResult.then((checkResult) => inst._zod.parse(checkResult, ctx));
			}
			return inst._zod.parse(checkResult, ctx);
		};
		inst._zod.run = (payload, ctx) => {
			if (ctx.skipChecks) return inst._zod.parse(payload, ctx);
			if (ctx.direction === "backward") {
				const canary = inst._zod.parse({
					value: payload.value,
					issues: []
				}, {
					...ctx,
					skipChecks: true
				});
				if (canary instanceof Promise) return canary.then((canary) => {
					return handleCanaryResult(canary, payload, ctx);
				});
				return handleCanaryResult(canary, payload, ctx);
			}
			const result = inst._zod.parse(payload, ctx);
			if (result instanceof Promise) {
				if (ctx.async === false) throw new $ZodAsyncError();
				return result.then((result) => runChecks(result, checks, ctx));
			}
			return runChecks(result, checks, ctx);
		};
	}
	defineLazy(inst, "~standard", () => ({
		validate: (value) => {
			try {
				const r = safeParse$1(inst, value);
				return r.success ? { value: r.data } : { issues: r.error?.issues };
			} catch (_) {
				return safeParseAsync$1(inst, value).then((r) => r.success ? { value: r.data } : { issues: r.error?.issues });
			}
		},
		vendor: "zod",
		version: 1
	}));
});
const $ZodString = /*@__PURE__*/ $constructor("$ZodString", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.pattern = [...inst?._zod.bag?.patterns ?? []].pop() ?? string$1(inst._zod.bag);
	inst._zod.parse = (payload, _) => {
		if (def.coerce) try {
			payload.value = String(payload.value);
		} catch (_) {}
		if (typeof payload.value === "string") return payload;
		payload.issues.push({
			expected: "string",
			code: "invalid_type",
			input: payload.value,
			inst
		});
		return payload;
	};
});
const $ZodStringFormat = /*@__PURE__*/ $constructor("$ZodStringFormat", (inst, def) => {
	$ZodCheckStringFormat.init(inst, def);
	$ZodString.init(inst, def);
});
const $ZodGUID = /*@__PURE__*/ $constructor("$ZodGUID", (inst, def) => {
	def.pattern ?? (def.pattern = guid);
	$ZodStringFormat.init(inst, def);
});
const $ZodUUID = /*@__PURE__*/ $constructor("$ZodUUID", (inst, def) => {
	if (def.version) {
		const v = {
			v1: 1,
			v2: 2,
			v3: 3,
			v4: 4,
			v5: 5,
			v6: 6,
			v7: 7,
			v8: 8
		}[def.version];
		if (v === void 0) throw new Error(`Invalid UUID version: "${def.version}"`);
		def.pattern ?? (def.pattern = uuid(v));
	} else def.pattern ?? (def.pattern = uuid());
	$ZodStringFormat.init(inst, def);
});
const $ZodEmail = /*@__PURE__*/ $constructor("$ZodEmail", (inst, def) => {
	def.pattern ?? (def.pattern = email);
	$ZodStringFormat.init(inst, def);
});
const $ZodURL = /*@__PURE__*/ $constructor("$ZodURL", (inst, def) => {
	$ZodStringFormat.init(inst, def);
	inst._zod.check = (payload) => {
		try {
			const trimmed = payload.value.trim();
			if (!def.normalize && def.protocol?.source === httpProtocol.source) {
				if (!/^https?:\/\//i.test(trimmed)) {
					payload.issues.push({
						code: "invalid_format",
						format: "url",
						note: "Invalid URL format",
						input: payload.value,
						inst,
						continue: !def.abort
					});
					return;
				}
			}
			const url = new URL(trimmed);
			if (def.hostname) {
				def.hostname.lastIndex = 0;
				if (!def.hostname.test(url.hostname)) payload.issues.push({
					code: "invalid_format",
					format: "url",
					note: "Invalid hostname",
					pattern: def.hostname.source,
					input: payload.value,
					inst,
					continue: !def.abort
				});
			}
			if (def.protocol) {
				def.protocol.lastIndex = 0;
				if (!def.protocol.test(url.protocol.endsWith(":") ? url.protocol.slice(0, -1) : url.protocol)) payload.issues.push({
					code: "invalid_format",
					format: "url",
					note: "Invalid protocol",
					pattern: def.protocol.source,
					input: payload.value,
					inst,
					continue: !def.abort
				});
			}
			if (def.normalize) payload.value = url.href;
			else payload.value = trimmed;
			return;
		} catch (_) {
			payload.issues.push({
				code: "invalid_format",
				format: "url",
				input: payload.value,
				inst,
				continue: !def.abort
			});
		}
	};
});
const $ZodEmoji = /*@__PURE__*/ $constructor("$ZodEmoji", (inst, def) => {
	def.pattern ?? (def.pattern = emoji());
	$ZodStringFormat.init(inst, def);
});
const $ZodNanoID = /*@__PURE__*/ $constructor("$ZodNanoID", (inst, def) => {
	def.pattern ?? (def.pattern = nanoid);
	$ZodStringFormat.init(inst, def);
});
/**
* @deprecated CUID v1 is deprecated by its authors due to information leakage
* (timestamps embedded in the id). Use {@link $ZodCUID2} instead.
* See https://github.com/paralleldrive/cuid.
*/
const $ZodCUID = /*@__PURE__*/ $constructor("$ZodCUID", (inst, def) => {
	def.pattern ?? (def.pattern = cuid);
	$ZodStringFormat.init(inst, def);
});
const $ZodCUID2 = /*@__PURE__*/ $constructor("$ZodCUID2", (inst, def) => {
	def.pattern ?? (def.pattern = cuid2);
	$ZodStringFormat.init(inst, def);
});
const $ZodULID = /*@__PURE__*/ $constructor("$ZodULID", (inst, def) => {
	def.pattern ?? (def.pattern = ulid);
	$ZodStringFormat.init(inst, def);
});
const $ZodXID = /*@__PURE__*/ $constructor("$ZodXID", (inst, def) => {
	def.pattern ?? (def.pattern = xid);
	$ZodStringFormat.init(inst, def);
});
const $ZodKSUID = /*@__PURE__*/ $constructor("$ZodKSUID", (inst, def) => {
	def.pattern ?? (def.pattern = ksuid);
	$ZodStringFormat.init(inst, def);
});
const $ZodISODateTime = /*@__PURE__*/ $constructor("$ZodISODateTime", (inst, def) => {
	def.pattern ?? (def.pattern = datetime$1(def));
	$ZodStringFormat.init(inst, def);
});
const $ZodISODate = /*@__PURE__*/ $constructor("$ZodISODate", (inst, def) => {
	def.pattern ?? (def.pattern = date$1);
	$ZodStringFormat.init(inst, def);
});
const $ZodISOTime = /*@__PURE__*/ $constructor("$ZodISOTime", (inst, def) => {
	def.pattern ?? (def.pattern = time$1(def));
	$ZodStringFormat.init(inst, def);
});
const $ZodISODuration = /*@__PURE__*/ $constructor("$ZodISODuration", (inst, def) => {
	def.pattern ?? (def.pattern = duration$1);
	$ZodStringFormat.init(inst, def);
});
const $ZodIPv4 = /*@__PURE__*/ $constructor("$ZodIPv4", (inst, def) => {
	def.pattern ?? (def.pattern = ipv4);
	$ZodStringFormat.init(inst, def);
	inst._zod.bag.format = `ipv4`;
});
const $ZodIPv6 = /*@__PURE__*/ $constructor("$ZodIPv6", (inst, def) => {
	def.pattern ?? (def.pattern = ipv6);
	$ZodStringFormat.init(inst, def);
	inst._zod.bag.format = `ipv6`;
	inst._zod.check = (payload) => {
		try {
			new URL(`http://[${payload.value}]`);
		} catch {
			payload.issues.push({
				code: "invalid_format",
				format: "ipv6",
				input: payload.value,
				inst,
				continue: !def.abort
			});
		}
	};
});
const $ZodCIDRv4 = /*@__PURE__*/ $constructor("$ZodCIDRv4", (inst, def) => {
	def.pattern ?? (def.pattern = cidrv4);
	$ZodStringFormat.init(inst, def);
});
const $ZodCIDRv6 = /*@__PURE__*/ $constructor("$ZodCIDRv6", (inst, def) => {
	def.pattern ?? (def.pattern = cidrv6);
	$ZodStringFormat.init(inst, def);
	inst._zod.check = (payload) => {
		const parts = payload.value.split("/");
		try {
			if (parts.length !== 2) throw new Error();
			const [address, prefix] = parts;
			if (!prefix) throw new Error();
			const prefixNum = Number(prefix);
			if (`${prefixNum}` !== prefix) throw new Error();
			if (prefixNum < 0 || prefixNum > 128) throw new Error();
			new URL(`http://[${address}]`);
		} catch {
			payload.issues.push({
				code: "invalid_format",
				format: "cidrv6",
				input: payload.value,
				inst,
				continue: !def.abort
			});
		}
	};
});
function isValidBase64(data) {
	if (data === "") return true;
	if (/\s/.test(data)) return false;
	if (data.length % 4 !== 0) return false;
	try {
		atob(data);
		return true;
	} catch {
		return false;
	}
}
const $ZodBase64 = /*@__PURE__*/ $constructor("$ZodBase64", (inst, def) => {
	def.pattern ?? (def.pattern = base64);
	$ZodStringFormat.init(inst, def);
	inst._zod.bag.contentEncoding = "base64";
	inst._zod.check = (payload) => {
		if (isValidBase64(payload.value)) return;
		payload.issues.push({
			code: "invalid_format",
			format: "base64",
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
function isValidBase64URL(data) {
	if (!base64url.test(data)) return false;
	const base64 = data.replace(/[-_]/g, (c) => c === "-" ? "+" : "/");
	return isValidBase64(base64.padEnd(Math.ceil(base64.length / 4) * 4, "="));
}
const $ZodBase64URL = /*@__PURE__*/ $constructor("$ZodBase64URL", (inst, def) => {
	def.pattern ?? (def.pattern = base64url);
	$ZodStringFormat.init(inst, def);
	inst._zod.bag.contentEncoding = "base64url";
	inst._zod.check = (payload) => {
		if (isValidBase64URL(payload.value)) return;
		payload.issues.push({
			code: "invalid_format",
			format: "base64url",
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodE164 = /*@__PURE__*/ $constructor("$ZodE164", (inst, def) => {
	def.pattern ?? (def.pattern = e164);
	$ZodStringFormat.init(inst, def);
});
function isValidJWT(token, algorithm = null) {
	try {
		const tokensParts = token.split(".");
		if (tokensParts.length !== 3) return false;
		const [header] = tokensParts;
		if (!header) return false;
		const parsedHeader = JSON.parse(atob(header));
		if ("typ" in parsedHeader && parsedHeader?.typ !== "JWT") return false;
		if (!parsedHeader.alg) return false;
		if (algorithm && (!("alg" in parsedHeader) || parsedHeader.alg !== algorithm)) return false;
		return true;
	} catch {
		return false;
	}
}
const $ZodJWT = /*@__PURE__*/ $constructor("$ZodJWT", (inst, def) => {
	$ZodStringFormat.init(inst, def);
	inst._zod.check = (payload) => {
		if (isValidJWT(payload.value, def.alg)) return;
		payload.issues.push({
			code: "invalid_format",
			format: "jwt",
			input: payload.value,
			inst,
			continue: !def.abort
		});
	};
});
const $ZodNumber = /*@__PURE__*/ $constructor("$ZodNumber", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.pattern = inst._zod.bag.pattern ?? number$1;
	inst._zod.parse = (payload, _ctx) => {
		if (def.coerce) try {
			payload.value = Number(payload.value);
		} catch (_) {}
		const input = payload.value;
		if (typeof input === "number" && !Number.isNaN(input) && Number.isFinite(input)) return payload;
		const received = typeof input === "number" ? Number.isNaN(input) ? "NaN" : !Number.isFinite(input) ? "Infinity" : void 0 : void 0;
		payload.issues.push({
			expected: "number",
			code: "invalid_type",
			input,
			inst,
			...received ? { received } : {}
		});
		return payload;
	};
});
const $ZodNumberFormat = /*@__PURE__*/ $constructor("$ZodNumberFormat", (inst, def) => {
	$ZodCheckNumberFormat.init(inst, def);
	$ZodNumber.init(inst, def);
});
const $ZodBoolean = /*@__PURE__*/ $constructor("$ZodBoolean", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.pattern = boolean$1;
	inst._zod.parse = (payload, _ctx) => {
		if (def.coerce) try {
			payload.value = Boolean(payload.value);
		} catch (_) {}
		const input = payload.value;
		if (typeof input === "boolean") return payload;
		payload.issues.push({
			expected: "boolean",
			code: "invalid_type",
			input,
			inst
		});
		return payload;
	};
});
const $ZodUnknown = /*@__PURE__*/ $constructor("$ZodUnknown", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.parse = (payload) => payload;
});
const $ZodNever = /*@__PURE__*/ $constructor("$ZodNever", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.parse = (payload, _ctx) => {
		payload.issues.push({
			expected: "never",
			code: "invalid_type",
			input: payload.value,
			inst
		});
		return payload;
	};
});
function handleArrayResult(result, final, index) {
	if (result.issues.length) final.issues.push(...prefixIssues(index, result.issues));
	final.value[index] = result.value;
}
const $ZodArray = /*@__PURE__*/ $constructor("$ZodArray", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.parse = (payload, ctx) => {
		const input = payload.value;
		if (!Array.isArray(input)) {
			payload.issues.push({
				expected: "array",
				code: "invalid_type",
				input,
				inst
			});
			return payload;
		}
		payload.value = Array(input.length);
		const proms = [];
		for (let i = 0; i < input.length; i++) {
			const item = input[i];
			const result = def.element._zod.run({
				value: item,
				issues: []
			}, ctx);
			if (result instanceof Promise) proms.push(result.then((result) => handleArrayResult(result, payload, i)));
			else handleArrayResult(result, payload, i);
		}
		if (proms.length) return Promise.all(proms).then(() => payload);
		return payload;
	};
});
function handlePropertyResult(result, final, key, input, isOptionalIn, isOptionalOut) {
	const isPresent = key in input;
	if (result.issues.length) {
		if (isOptionalIn && isOptionalOut && !isPresent) return;
		final.issues.push(...prefixIssues(key, result.issues));
	}
	if (!isPresent && !isOptionalIn) {
		if (!result.issues.length) final.issues.push({
			code: "invalid_type",
			expected: "nonoptional",
			input: void 0,
			path: [key]
		});
		return;
	}
	if (result.value === void 0) {
		if (isPresent) final.value[key] = void 0;
	} else final.value[key] = result.value;
}
function normalizeDef(def) {
	const keys = Object.keys(def.shape);
	for (const k of keys) if (!def.shape?.[k]?._zod?.traits?.has("$ZodType")) throw new Error(`Invalid element at key "${k}": expected a Zod schema`);
	const okeys = optionalKeys(def.shape);
	return {
		...def,
		keys,
		keySet: new Set(keys),
		numKeys: keys.length,
		optionalKeys: new Set(okeys)
	};
}
function handleCatchall(proms, input, payload, ctx, def, inst) {
	const unrecognized = [];
	const keySet = def.keySet;
	const _catchall = def.catchall._zod;
	const t = _catchall.def.type;
	const isOptionalIn = _catchall.optin === "optional";
	const isOptionalOut = _catchall.optout === "optional";
	for (const key in input) {
		if (key === "__proto__") continue;
		if (keySet.has(key)) continue;
		if (t === "never") {
			unrecognized.push(key);
			continue;
		}
		const r = _catchall.run({
			value: input[key],
			issues: []
		}, ctx);
		if (r instanceof Promise) proms.push(r.then((r) => handlePropertyResult(r, payload, key, input, isOptionalIn, isOptionalOut)));
		else handlePropertyResult(r, payload, key, input, isOptionalIn, isOptionalOut);
	}
	if (unrecognized.length) payload.issues.push({
		code: "unrecognized_keys",
		keys: unrecognized,
		input,
		inst
	});
	if (!proms.length) return payload;
	return Promise.all(proms).then(() => {
		return payload;
	});
}
const $ZodObject = /*@__PURE__*/ $constructor("$ZodObject", (inst, def) => {
	$ZodType.init(inst, def);
	if (!Object.getOwnPropertyDescriptor(def, "shape")?.get) {
		const sh = def.shape;
		Object.defineProperty(def, "shape", { get: () => {
			const newSh = { ...sh };
			Object.defineProperty(def, "shape", { value: newSh });
			return newSh;
		} });
	}
	const _normalized = cached(() => normalizeDef(def));
	defineLazy(inst._zod, "propValues", () => {
		const shape = def.shape;
		const propValues = {};
		for (const key in shape) {
			const field = shape[key]._zod;
			if (field.values) {
				propValues[key] ?? (propValues[key] = /* @__PURE__ */ new Set());
				for (const v of field.values) propValues[key].add(v);
			}
		}
		return propValues;
	});
	const isObject$1 = isObject;
	const catchall = def.catchall;
	let value;
	inst._zod.parse = (payload, ctx) => {
		value ?? (value = _normalized.value);
		const input = payload.value;
		if (!isObject$1(input)) {
			payload.issues.push({
				expected: "object",
				code: "invalid_type",
				input,
				inst
			});
			return payload;
		}
		payload.value = {};
		const proms = [];
		const shape = value.shape;
		for (const key of value.keys) {
			const el = shape[key];
			const isOptionalIn = el._zod.optin === "optional";
			const isOptionalOut = el._zod.optout === "optional";
			const r = el._zod.run({
				value: input[key],
				issues: []
			}, ctx);
			if (r instanceof Promise) proms.push(r.then((r) => handlePropertyResult(r, payload, key, input, isOptionalIn, isOptionalOut)));
			else handlePropertyResult(r, payload, key, input, isOptionalIn, isOptionalOut);
		}
		if (!catchall) return proms.length ? Promise.all(proms).then(() => payload) : payload;
		return handleCatchall(proms, input, payload, ctx, _normalized.value, inst);
	};
});
const $ZodObjectJIT = /*@__PURE__*/ $constructor("$ZodObjectJIT", (inst, def) => {
	$ZodObject.init(inst, def);
	const superParse = inst._zod.parse;
	const _normalized = cached(() => normalizeDef(def));
	const generateFastpass = (shape) => {
		const doc = new Doc([
			"shape",
			"payload",
			"ctx"
		]);
		const normalized = _normalized.value;
		const parseStr = (key) => {
			const k = esc(key);
			return `shape[${k}]._zod.run({ value: input[${k}], issues: [] }, ctx)`;
		};
		doc.write(`const input = payload.value;`);
		const ids = Object.create(null);
		let counter = 0;
		for (const key of normalized.keys) ids[key] = `key_${counter++}`;
		doc.write(`const newResult = {};`);
		for (const key of normalized.keys) {
			const id = ids[key];
			const k = esc(key);
			const schema = shape[key];
			const isOptionalIn = schema?._zod?.optin === "optional";
			const isOptionalOut = schema?._zod?.optout === "optional";
			doc.write(`const ${id} = ${parseStr(key)};`);
			if (isOptionalIn && isOptionalOut) doc.write(`
        if (${id}.issues.length) {
          if (${k} in input) {
            payload.issues = payload.issues.concat(${id}.issues.map(iss => ({
              ...iss,
              path: iss.path ? [${k}, ...iss.path] : [${k}]
            })));
          }
        }

        if (${id}.value === undefined) {
          if (${k} in input) {
            newResult[${k}] = undefined;
          }
        } else {
          newResult[${k}] = ${id}.value;
        }

      `);
			else if (!isOptionalIn) doc.write(`
        const ${id}_present = ${k} in input;
        if (${id}.issues.length) {
          payload.issues = payload.issues.concat(${id}.issues.map(iss => ({
            ...iss,
            path: iss.path ? [${k}, ...iss.path] : [${k}]
          })));
        }
        if (!${id}_present && !${id}.issues.length) {
          payload.issues.push({
            code: "invalid_type",
            expected: "nonoptional",
            input: undefined,
            path: [${k}]
          });
        }

        if (${id}_present) {
          if (${id}.value === undefined) {
            newResult[${k}] = undefined;
          } else {
            newResult[${k}] = ${id}.value;
          }
        }

      `);
			else doc.write(`
        if (${id}.issues.length) {
          payload.issues = payload.issues.concat(${id}.issues.map(iss => ({
            ...iss,
            path: iss.path ? [${k}, ...iss.path] : [${k}]
          })));
        }

        if (${id}.value === undefined) {
          if (${k} in input) {
            newResult[${k}] = undefined;
          }
        } else {
          newResult[${k}] = ${id}.value;
        }

      `);
		}
		doc.write(`payload.value = newResult;`);
		doc.write(`return payload;`);
		const fn = doc.compile();
		return (payload, ctx) => fn(shape, payload, ctx);
	};
	let fastpass;
	const isObject$2 = isObject;
	const jit = !globalConfig.jitless;
	const fastEnabled = jit && allowsEval.value;
	const catchall = def.catchall;
	let value;
	inst._zod.parse = (payload, ctx) => {
		value ?? (value = _normalized.value);
		const input = payload.value;
		if (!isObject$2(input)) {
			payload.issues.push({
				expected: "object",
				code: "invalid_type",
				input,
				inst
			});
			return payload;
		}
		if (jit && fastEnabled && ctx?.async === false && ctx.jitless !== true) {
			if (!fastpass) fastpass = generateFastpass(def.shape);
			payload = fastpass(payload, ctx);
			if (!catchall) return payload;
			return handleCatchall([], input, payload, ctx, value, inst);
		}
		return superParse(payload, ctx);
	};
});
function handleUnionResults(results, final, inst, ctx) {
	for (const result of results) if (result.issues.length === 0) {
		final.value = result.value;
		return final;
	}
	const nonaborted = results.filter((r) => !aborted(r));
	if (nonaborted.length === 1) {
		final.value = nonaborted[0].value;
		return nonaborted[0];
	}
	final.issues.push({
		code: "invalid_union",
		input: final.value,
		inst,
		errors: results.map((result) => result.issues.map((iss) => finalizeIssue(iss, ctx, config())))
	});
	return final;
}
const $ZodUnion = /*@__PURE__*/ $constructor("$ZodUnion", (inst, def) => {
	$ZodType.init(inst, def);
	defineLazy(inst._zod, "optin", () => def.options.some((o) => o._zod.optin === "optional") ? "optional" : void 0);
	defineLazy(inst._zod, "optout", () => def.options.some((o) => o._zod.optout === "optional") ? "optional" : void 0);
	defineLazy(inst._zod, "values", () => {
		if (def.options.every((o) => o._zod.values)) return new Set(def.options.flatMap((option) => Array.from(option._zod.values)));
	});
	defineLazy(inst._zod, "pattern", () => {
		if (def.options.every((o) => o._zod.pattern)) {
			const patterns = def.options.map((o) => o._zod.pattern);
			return new RegExp(`^(${patterns.map((p) => cleanRegex(p.source)).join("|")})$`);
		}
	});
	const first = def.options.length === 1 ? def.options[0]._zod.run : null;
	inst._zod.parse = (payload, ctx) => {
		if (first) return first(payload, ctx);
		let async = false;
		const results = [];
		for (const option of def.options) {
			const result = option._zod.run({
				value: payload.value,
				issues: []
			}, ctx);
			if (result instanceof Promise) {
				results.push(result);
				async = true;
			} else {
				if (result.issues.length === 0) return result;
				results.push(result);
			}
		}
		if (!async) return handleUnionResults(results, payload, inst, ctx);
		return Promise.all(results).then((results) => {
			return handleUnionResults(results, payload, inst, ctx);
		});
	};
});
const $ZodIntersection = /*@__PURE__*/ $constructor("$ZodIntersection", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.parse = (payload, ctx) => {
		const input = payload.value;
		const left = def.left._zod.run({
			value: input,
			issues: []
		}, ctx);
		const right = def.right._zod.run({
			value: input,
			issues: []
		}, ctx);
		if (left instanceof Promise || right instanceof Promise) return Promise.all([left, right]).then(([left, right]) => {
			return handleIntersectionResults(payload, left, right);
		});
		return handleIntersectionResults(payload, left, right);
	};
});
function mergeValues(a, b) {
	if (a === b) return {
		valid: true,
		data: a
	};
	if (a instanceof Date && b instanceof Date && +a === +b) return {
		valid: true,
		data: a
	};
	if (isPlainObject(a) && isPlainObject(b)) {
		const bKeys = Object.keys(b);
		const sharedKeys = Object.keys(a).filter((key) => bKeys.indexOf(key) !== -1);
		const newObj = {
			...a,
			...b
		};
		for (const key of sharedKeys) {
			const sharedValue = mergeValues(a[key], b[key]);
			if (!sharedValue.valid) return {
				valid: false,
				mergeErrorPath: [key, ...sharedValue.mergeErrorPath]
			};
			newObj[key] = sharedValue.data;
		}
		return {
			valid: true,
			data: newObj
		};
	}
	if (Array.isArray(a) && Array.isArray(b)) {
		if (a.length !== b.length) return {
			valid: false,
			mergeErrorPath: []
		};
		const newArray = [];
		for (let index = 0; index < a.length; index++) {
			const itemA = a[index];
			const itemB = b[index];
			const sharedValue = mergeValues(itemA, itemB);
			if (!sharedValue.valid) return {
				valid: false,
				mergeErrorPath: [index, ...sharedValue.mergeErrorPath]
			};
			newArray.push(sharedValue.data);
		}
		return {
			valid: true,
			data: newArray
		};
	}
	return {
		valid: false,
		mergeErrorPath: []
	};
}
function handleIntersectionResults(result, left, right) {
	const unrecKeys = /* @__PURE__ */ new Map();
	let unrecIssue;
	for (const iss of left.issues) if (iss.code === "unrecognized_keys") {
		unrecIssue ?? (unrecIssue = iss);
		for (const k of iss.keys) {
			if (!unrecKeys.has(k)) unrecKeys.set(k, {});
			unrecKeys.get(k).l = true;
		}
	} else result.issues.push(iss);
	for (const iss of right.issues) if (iss.code === "unrecognized_keys") for (const k of iss.keys) {
		if (!unrecKeys.has(k)) unrecKeys.set(k, {});
		unrecKeys.get(k).r = true;
	}
	else result.issues.push(iss);
	const bothKeys = [...unrecKeys].filter(([, f]) => f.l && f.r).map(([k]) => k);
	if (bothKeys.length && unrecIssue) result.issues.push({
		...unrecIssue,
		keys: bothKeys
	});
	if (aborted(result)) return result;
	const merged = mergeValues(left.value, right.value);
	if (!merged.valid) throw new Error(`Unmergable intersection. Error path: ${JSON.stringify(merged.mergeErrorPath)}`);
	result.value = merged.data;
	return result;
}
const $ZodEnum = /*@__PURE__*/ $constructor("$ZodEnum", (inst, def) => {
	$ZodType.init(inst, def);
	const values = getEnumValues(def.entries);
	const valuesSet = new Set(values);
	inst._zod.values = valuesSet;
	inst._zod.pattern = new RegExp(`^(${values.filter((k) => propertyKeyTypes.has(typeof k)).map((o) => typeof o === "string" ? escapeRegex(o) : o.toString()).join("|")})$`);
	inst._zod.parse = (payload, _ctx) => {
		const input = payload.value;
		if (valuesSet.has(input)) return payload;
		payload.issues.push({
			code: "invalid_value",
			values,
			input,
			inst
		});
		return payload;
	};
});
const $ZodLiteral = /*@__PURE__*/ $constructor("$ZodLiteral", (inst, def) => {
	$ZodType.init(inst, def);
	if (def.values.length === 0) throw new Error("Cannot create literal schema with no valid values");
	const values = new Set(def.values);
	inst._zod.values = values;
	inst._zod.pattern = new RegExp(`^(${def.values.map((o) => typeof o === "string" ? escapeRegex(o) : o ? escapeRegex(o.toString()) : String(o)).join("|")})$`);
	inst._zod.parse = (payload, _ctx) => {
		const input = payload.value;
		if (values.has(input)) return payload;
		payload.issues.push({
			code: "invalid_value",
			values: def.values,
			input,
			inst
		});
		return payload;
	};
});
const $ZodTransform = /*@__PURE__*/ $constructor("$ZodTransform", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.optin = "optional";
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") throw new $ZodEncodeError(inst.constructor.name);
		const _out = def.transform(payload.value, payload);
		if (ctx.async) return (_out instanceof Promise ? _out : Promise.resolve(_out)).then((output) => {
			payload.value = output;
			payload.fallback = true;
			return payload;
		});
		if (_out instanceof Promise) throw new $ZodAsyncError();
		payload.value = _out;
		payload.fallback = true;
		return payload;
	};
});
function handleOptionalResult(result, input) {
	if (input === void 0 && (result.issues.length || result.fallback)) return {
		issues: [],
		value: void 0
	};
	return result;
}
const $ZodOptional = /*@__PURE__*/ $constructor("$ZodOptional", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.optin = "optional";
	inst._zod.optout = "optional";
	defineLazy(inst._zod, "values", () => {
		return def.innerType._zod.values ? new Set([...def.innerType._zod.values, void 0]) : void 0;
	});
	defineLazy(inst._zod, "pattern", () => {
		const pattern = def.innerType._zod.pattern;
		return pattern ? new RegExp(`^(${cleanRegex(pattern.source)})?$`) : void 0;
	});
	inst._zod.parse = (payload, ctx) => {
		if (def.innerType._zod.optin === "optional") {
			const input = payload.value;
			const result = def.innerType._zod.run(payload, ctx);
			if (result instanceof Promise) return result.then((r) => handleOptionalResult(r, input));
			return handleOptionalResult(result, input);
		}
		if (payload.value === void 0) return payload;
		return def.innerType._zod.run(payload, ctx);
	};
});
const $ZodExactOptional = /*@__PURE__*/ $constructor("$ZodExactOptional", (inst, def) => {
	$ZodOptional.init(inst, def);
	defineLazy(inst._zod, "values", () => def.innerType._zod.values);
	defineLazy(inst._zod, "pattern", () => def.innerType._zod.pattern);
	inst._zod.parse = (payload, ctx) => {
		return def.innerType._zod.run(payload, ctx);
	};
});
const $ZodNullable = /*@__PURE__*/ $constructor("$ZodNullable", (inst, def) => {
	$ZodType.init(inst, def);
	defineLazy(inst._zod, "optin", () => def.innerType._zod.optin);
	defineLazy(inst._zod, "optout", () => def.innerType._zod.optout);
	defineLazy(inst._zod, "pattern", () => {
		const pattern = def.innerType._zod.pattern;
		return pattern ? new RegExp(`^(${cleanRegex(pattern.source)}|null)$`) : void 0;
	});
	defineLazy(inst._zod, "values", () => {
		return def.innerType._zod.values ? new Set([...def.innerType._zod.values, null]) : void 0;
	});
	inst._zod.parse = (payload, ctx) => {
		if (payload.value === null) return payload;
		return def.innerType._zod.run(payload, ctx);
	};
});
const $ZodDefault = /*@__PURE__*/ $constructor("$ZodDefault", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.optin = "optional";
	defineLazy(inst._zod, "values", () => def.innerType._zod.values);
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") return def.innerType._zod.run(payload, ctx);
		if (payload.value === void 0) {
			payload.value = def.defaultValue;
			/**
			* $ZodDefault returns the default value immediately in forward direction.
			* It doesn't pass the default value into the validator ("prefault"). There's no reason to pass the default value through validation. The validity of the default is enforced by TypeScript statically. Otherwise, it's the responsibility of the user to ensure the default is valid. In the case of pipes with divergent in/out types, you can specify the default on the `in` schema of your ZodPipe to set a "prefault" for the pipe.   */
			return payload;
		}
		const result = def.innerType._zod.run(payload, ctx);
		if (result instanceof Promise) return result.then((result) => handleDefaultResult(result, def));
		return handleDefaultResult(result, def);
	};
});
function handleDefaultResult(payload, def) {
	if (payload.value === void 0) payload.value = def.defaultValue;
	return payload;
}
const $ZodPrefault = /*@__PURE__*/ $constructor("$ZodPrefault", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.optin = "optional";
	defineLazy(inst._zod, "values", () => def.innerType._zod.values);
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") return def.innerType._zod.run(payload, ctx);
		if (payload.value === void 0) payload.value = def.defaultValue;
		return def.innerType._zod.run(payload, ctx);
	};
});
const $ZodNonOptional = /*@__PURE__*/ $constructor("$ZodNonOptional", (inst, def) => {
	$ZodType.init(inst, def);
	defineLazy(inst._zod, "values", () => {
		const v = def.innerType._zod.values;
		return v ? new Set([...v].filter((x) => x !== void 0)) : void 0;
	});
	inst._zod.parse = (payload, ctx) => {
		const result = def.innerType._zod.run(payload, ctx);
		if (result instanceof Promise) return result.then((result) => handleNonOptionalResult(result, inst));
		return handleNonOptionalResult(result, inst);
	};
});
function handleNonOptionalResult(payload, inst) {
	if (!payload.issues.length && payload.value === void 0) payload.issues.push({
		code: "invalid_type",
		expected: "nonoptional",
		input: payload.value,
		inst
	});
	return payload;
}
const $ZodCatch = /*@__PURE__*/ $constructor("$ZodCatch", (inst, def) => {
	$ZodType.init(inst, def);
	inst._zod.optin = "optional";
	defineLazy(inst._zod, "optout", () => def.innerType._zod.optout);
	defineLazy(inst._zod, "values", () => def.innerType._zod.values);
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") return def.innerType._zod.run(payload, ctx);
		const result = def.innerType._zod.run(payload, ctx);
		if (result instanceof Promise) return result.then((result) => {
			payload.value = result.value;
			if (result.issues.length) {
				payload.value = def.catchValue({
					...payload,
					error: { issues: result.issues.map((iss) => finalizeIssue(iss, ctx, config())) },
					input: payload.value
				});
				payload.issues = [];
				payload.fallback = true;
			}
			return payload;
		});
		payload.value = result.value;
		if (result.issues.length) {
			payload.value = def.catchValue({
				...payload,
				error: { issues: result.issues.map((iss) => finalizeIssue(iss, ctx, config())) },
				input: payload.value
			});
			payload.issues = [];
			payload.fallback = true;
		}
		return payload;
	};
});
const $ZodPipe = /*@__PURE__*/ $constructor("$ZodPipe", (inst, def) => {
	$ZodType.init(inst, def);
	defineLazy(inst._zod, "values", () => def.in._zod.values);
	defineLazy(inst._zod, "optin", () => def.in._zod.optin);
	defineLazy(inst._zod, "optout", () => def.out._zod.optout);
	defineLazy(inst._zod, "propValues", () => def.in._zod.propValues);
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") {
			const right = def.out._zod.run(payload, ctx);
			if (right instanceof Promise) return right.then((right) => handlePipeResult(right, def.in, ctx));
			return handlePipeResult(right, def.in, ctx);
		}
		const left = def.in._zod.run(payload, ctx);
		if (left instanceof Promise) return left.then((left) => handlePipeResult(left, def.out, ctx));
		return handlePipeResult(left, def.out, ctx);
	};
});
function handlePipeResult(left, next, ctx) {
	if (left.issues.length) {
		left.aborted = true;
		return left;
	}
	return next._zod.run({
		value: left.value,
		issues: left.issues,
		fallback: left.fallback
	}, ctx);
}
const $ZodReadonly = /*@__PURE__*/ $constructor("$ZodReadonly", (inst, def) => {
	$ZodType.init(inst, def);
	defineLazy(inst._zod, "propValues", () => def.innerType._zod.propValues);
	defineLazy(inst._zod, "values", () => def.innerType._zod.values);
	defineLazy(inst._zod, "optin", () => def.innerType?._zod?.optin);
	defineLazy(inst._zod, "optout", () => def.innerType?._zod?.optout);
	inst._zod.parse = (payload, ctx) => {
		if (ctx.direction === "backward") return def.innerType._zod.run(payload, ctx);
		const result = def.innerType._zod.run(payload, ctx);
		if (result instanceof Promise) return result.then(handleReadonlyResult);
		return handleReadonlyResult(result);
	};
});
function handleReadonlyResult(payload) {
	payload.value = Object.freeze(payload.value);
	return payload;
}
const $ZodCustom = /*@__PURE__*/ $constructor("$ZodCustom", (inst, def) => {
	$ZodCheck.init(inst, def);
	$ZodType.init(inst, def);
	inst._zod.parse = (payload, _) => {
		return payload;
	};
	inst._zod.check = (payload) => {
		const input = payload.value;
		const r = def.fn(input);
		if (r instanceof Promise) return r.then((r) => handleRefineResult(r, payload, input, inst));
		handleRefineResult(r, payload, input, inst);
	};
});
function handleRefineResult(result, payload, input, inst) {
	if (!result) {
		const _iss = {
			code: "custom",
			input,
			inst,
			path: [...inst._zod.def.path ?? []],
			continue: !inst._zod.def.abort
		};
		if (inst._zod.def.params) _iss.params = inst._zod.def.params;
		payload.issues.push(issue(_iss));
	}
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/registries.js
var _a;
var $ZodRegistry = class {
	constructor() {
		this._map = /* @__PURE__ */ new WeakMap();
		this._idmap = /* @__PURE__ */ new Map();
	}
	add(schema, ..._meta) {
		const meta = _meta[0];
		this._map.set(schema, meta);
		if (meta && typeof meta === "object" && "id" in meta) this._idmap.set(meta.id, schema);
		return this;
	}
	clear() {
		this._map = /* @__PURE__ */ new WeakMap();
		this._idmap = /* @__PURE__ */ new Map();
		return this;
	}
	remove(schema) {
		const meta = this._map.get(schema);
		if (meta && typeof meta === "object" && "id" in meta) this._idmap.delete(meta.id);
		this._map.delete(schema);
		return this;
	}
	get(schema) {
		const p = schema._zod.parent;
		if (p) {
			const pm = { ...this.get(p) ?? {} };
			delete pm.id;
			const f = {
				...pm,
				...this._map.get(schema)
			};
			return Object.keys(f).length ? f : void 0;
		}
		return this._map.get(schema);
	}
	has(schema) {
		return this._map.has(schema);
	}
};
function registry() {
	return new $ZodRegistry();
}
(_a = globalThis).__zod_globalRegistry ?? (_a.__zod_globalRegistry = registry());
const globalRegistry = globalThis.__zod_globalRegistry;
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/api.js
// @__NO_SIDE_EFFECTS__
function _string(Class, params) {
	return new Class({
		type: "string",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _email(Class, params) {
	return new Class({
		type: "string",
		format: "email",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _guid(Class, params) {
	return new Class({
		type: "string",
		format: "guid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _uuid(Class, params) {
	return new Class({
		type: "string",
		format: "uuid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _uuidv4(Class, params) {
	return new Class({
		type: "string",
		format: "uuid",
		check: "string_format",
		abort: false,
		version: "v4",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _uuidv6(Class, params) {
	return new Class({
		type: "string",
		format: "uuid",
		check: "string_format",
		abort: false,
		version: "v6",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _uuidv7(Class, params) {
	return new Class({
		type: "string",
		format: "uuid",
		check: "string_format",
		abort: false,
		version: "v7",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _url(Class, params) {
	return new Class({
		type: "string",
		format: "url",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _emoji(Class, params) {
	return new Class({
		type: "string",
		format: "emoji",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _nanoid(Class, params) {
	return new Class({
		type: "string",
		format: "nanoid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
/**
* @deprecated CUID v1 is deprecated by its authors due to information leakage
* (timestamps embedded in the id). Use {@link _cuid2} instead.
* See https://github.com/paralleldrive/cuid.
*/
// @__NO_SIDE_EFFECTS__
function _cuid(Class, params) {
	return new Class({
		type: "string",
		format: "cuid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _cuid2(Class, params) {
	return new Class({
		type: "string",
		format: "cuid2",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _ulid(Class, params) {
	return new Class({
		type: "string",
		format: "ulid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _xid(Class, params) {
	return new Class({
		type: "string",
		format: "xid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _ksuid(Class, params) {
	return new Class({
		type: "string",
		format: "ksuid",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _ipv4(Class, params) {
	return new Class({
		type: "string",
		format: "ipv4",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _ipv6(Class, params) {
	return new Class({
		type: "string",
		format: "ipv6",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _cidrv4(Class, params) {
	return new Class({
		type: "string",
		format: "cidrv4",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _cidrv6(Class, params) {
	return new Class({
		type: "string",
		format: "cidrv6",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _base64(Class, params) {
	return new Class({
		type: "string",
		format: "base64",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _base64url(Class, params) {
	return new Class({
		type: "string",
		format: "base64url",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _e164(Class, params) {
	return new Class({
		type: "string",
		format: "e164",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _jwt(Class, params) {
	return new Class({
		type: "string",
		format: "jwt",
		check: "string_format",
		abort: false,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _isoDateTime(Class, params) {
	return new Class({
		type: "string",
		format: "datetime",
		check: "string_format",
		offset: false,
		local: false,
		precision: null,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _isoDate(Class, params) {
	return new Class({
		type: "string",
		format: "date",
		check: "string_format",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _isoTime(Class, params) {
	return new Class({
		type: "string",
		format: "time",
		check: "string_format",
		precision: null,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _isoDuration(Class, params) {
	return new Class({
		type: "string",
		format: "duration",
		check: "string_format",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _number(Class, params) {
	return new Class({
		type: "number",
		checks: [],
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _int(Class, params) {
	return new Class({
		type: "number",
		check: "number_format",
		abort: false,
		format: "safeint",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _boolean(Class, params) {
	return new Class({
		type: "boolean",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _unknown(Class) {
	return new Class({ type: "unknown" });
}
// @__NO_SIDE_EFFECTS__
function _never(Class, params) {
	return new Class({
		type: "never",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _lt(value, params) {
	return new $ZodCheckLessThan({
		check: "less_than",
		...normalizeParams(params),
		value,
		inclusive: false
	});
}
// @__NO_SIDE_EFFECTS__
function _lte(value, params) {
	return new $ZodCheckLessThan({
		check: "less_than",
		...normalizeParams(params),
		value,
		inclusive: true
	});
}
// @__NO_SIDE_EFFECTS__
function _gt(value, params) {
	return new $ZodCheckGreaterThan({
		check: "greater_than",
		...normalizeParams(params),
		value,
		inclusive: false
	});
}
// @__NO_SIDE_EFFECTS__
function _gte(value, params) {
	return new $ZodCheckGreaterThan({
		check: "greater_than",
		...normalizeParams(params),
		value,
		inclusive: true
	});
}
// @__NO_SIDE_EFFECTS__
function _multipleOf(value, params) {
	return new $ZodCheckMultipleOf({
		check: "multiple_of",
		...normalizeParams(params),
		value
	});
}
// @__NO_SIDE_EFFECTS__
function _maxLength(maximum, params) {
	return new $ZodCheckMaxLength({
		check: "max_length",
		...normalizeParams(params),
		maximum
	});
}
// @__NO_SIDE_EFFECTS__
function _minLength(minimum, params) {
	return new $ZodCheckMinLength({
		check: "min_length",
		...normalizeParams(params),
		minimum
	});
}
// @__NO_SIDE_EFFECTS__
function _length(length, params) {
	return new $ZodCheckLengthEquals({
		check: "length_equals",
		...normalizeParams(params),
		length
	});
}
// @__NO_SIDE_EFFECTS__
function _regex(pattern, params) {
	return new $ZodCheckRegex({
		check: "string_format",
		format: "regex",
		...normalizeParams(params),
		pattern
	});
}
// @__NO_SIDE_EFFECTS__
function _lowercase(params) {
	return new $ZodCheckLowerCase({
		check: "string_format",
		format: "lowercase",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _uppercase(params) {
	return new $ZodCheckUpperCase({
		check: "string_format",
		format: "uppercase",
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _includes(includes, params) {
	return new $ZodCheckIncludes({
		check: "string_format",
		format: "includes",
		...normalizeParams(params),
		includes
	});
}
// @__NO_SIDE_EFFECTS__
function _startsWith(prefix, params) {
	return new $ZodCheckStartsWith({
		check: "string_format",
		format: "starts_with",
		...normalizeParams(params),
		prefix
	});
}
// @__NO_SIDE_EFFECTS__
function _endsWith(suffix, params) {
	return new $ZodCheckEndsWith({
		check: "string_format",
		format: "ends_with",
		...normalizeParams(params),
		suffix
	});
}
// @__NO_SIDE_EFFECTS__
function _overwrite(tx) {
	return new $ZodCheckOverwrite({
		check: "overwrite",
		tx
	});
}
// @__NO_SIDE_EFFECTS__
function _normalize(form) {
	return /* @__PURE__ */ _overwrite((input) => input.normalize(form));
}
// @__NO_SIDE_EFFECTS__
function _trim() {
	return /* @__PURE__ */ _overwrite((input) => input.trim());
}
// @__NO_SIDE_EFFECTS__
function _toLowerCase() {
	return /* @__PURE__ */ _overwrite((input) => input.toLowerCase());
}
// @__NO_SIDE_EFFECTS__
function _toUpperCase() {
	return /* @__PURE__ */ _overwrite((input) => input.toUpperCase());
}
// @__NO_SIDE_EFFECTS__
function _slugify() {
	return /* @__PURE__ */ _overwrite((input) => slugify(input));
}
// @__NO_SIDE_EFFECTS__
function _array(Class, element, params) {
	return new Class({
		type: "array",
		element,
		...normalizeParams(params)
	});
}
// @__NO_SIDE_EFFECTS__
function _refine(Class, fn, _params) {
	return new Class({
		type: "custom",
		check: "custom",
		fn,
		...normalizeParams(_params)
	});
}
// @__NO_SIDE_EFFECTS__
function _superRefine(fn, params) {
	const ch = /* @__PURE__ */ _check((payload) => {
		payload.addIssue = (issue$2) => {
			if (typeof issue$2 === "string") payload.issues.push(issue(issue$2, payload.value, ch._zod.def));
			else {
				const _issue = issue$2;
				if (_issue.fatal) _issue.continue = false;
				_issue.code ?? (_issue.code = "custom");
				_issue.input ?? (_issue.input = payload.value);
				_issue.inst ?? (_issue.inst = ch);
				_issue.continue ?? (_issue.continue = !ch._zod.def.abort);
				payload.issues.push(issue(_issue));
			}
		};
		return fn(payload.value, payload);
	}, params);
	return ch;
}
// @__NO_SIDE_EFFECTS__
function _check(fn, params) {
	const ch = new $ZodCheck({
		check: "custom",
		...normalizeParams(params)
	});
	ch._zod.check = fn;
	return ch;
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/to-json-schema.js
function initializeContext(params) {
	let target = params?.target ?? "draft-2020-12";
	if (target === "draft-4") target = "draft-04";
	if (target === "draft-7") target = "draft-07";
	return {
		processors: params.processors ?? {},
		metadataRegistry: params?.metadata ?? globalRegistry,
		target,
		unrepresentable: params?.unrepresentable ?? "throw",
		override: params?.override ?? (() => {}),
		io: params?.io ?? "output",
		counter: 0,
		seen: /* @__PURE__ */ new Map(),
		cycles: params?.cycles ?? "ref",
		reused: params?.reused ?? "inline",
		external: params?.external ?? void 0
	};
}
function process(schema, ctx, _params = {
	path: [],
	schemaPath: []
}) {
	var _a;
	const def = schema._zod.def;
	const seen = ctx.seen.get(schema);
	if (seen) {
		seen.count++;
		if (_params.schemaPath.includes(schema)) seen.cycle = _params.path;
		return seen.schema;
	}
	const result = {
		schema: {},
		count: 1,
		cycle: void 0,
		path: _params.path
	};
	ctx.seen.set(schema, result);
	const overrideSchema = schema._zod.toJSONSchema?.();
	if (overrideSchema) result.schema = overrideSchema;
	else {
		const params = {
			..._params,
			schemaPath: [..._params.schemaPath, schema],
			path: _params.path
		};
		if (schema._zod.processJSONSchema) schema._zod.processJSONSchema(ctx, result.schema, params);
		else {
			const _json = result.schema;
			const processor = ctx.processors[def.type];
			if (!processor) throw new Error(`[toJSONSchema]: Non-representable type encountered: ${def.type}`);
			processor(schema, ctx, _json, params);
		}
		const parent = schema._zod.parent;
		if (parent) {
			if (!result.ref) result.ref = parent;
			process(parent, ctx, params);
			ctx.seen.get(parent).isParent = true;
		}
	}
	const meta = ctx.metadataRegistry.get(schema);
	if (meta) Object.assign(result.schema, meta);
	if (ctx.io === "input" && isTransforming(schema)) {
		delete result.schema.examples;
		delete result.schema.default;
	}
	if (ctx.io === "input" && "_prefault" in result.schema) (_a = result.schema).default ?? (_a.default = result.schema._prefault);
	delete result.schema._prefault;
	return ctx.seen.get(schema).schema;
}
function extractDefs(ctx, schema) {
	const root = ctx.seen.get(schema);
	if (!root) throw new Error("Unprocessed schema. This is a bug in Zod.");
	const idToSchema = /* @__PURE__ */ new Map();
	for (const entry of ctx.seen.entries()) {
		const id = ctx.metadataRegistry.get(entry[0])?.id;
		if (id) {
			const existing = idToSchema.get(id);
			if (existing && existing !== entry[0]) throw new Error(`Duplicate schema id "${id}" detected during JSON Schema conversion. Two different schemas cannot share the same id when converted together.`);
			idToSchema.set(id, entry[0]);
		}
	}
	const makeURI = (entry) => {
		const defsSegment = ctx.target === "draft-2020-12" ? "$defs" : "definitions";
		if (ctx.external) {
			const externalId = ctx.external.registry.get(entry[0])?.id;
			const uriGenerator = ctx.external.uri ?? ((id) => id);
			if (externalId) return { ref: uriGenerator(externalId) };
			const id = entry[1].defId ?? entry[1].schema.id ?? `schema${ctx.counter++}`;
			entry[1].defId = id;
			return {
				defId: id,
				ref: `${uriGenerator("__shared")}#/${defsSegment}/${id}`
			};
		}
		if (entry[1] === root) return { ref: "#" };
		const defUriPrefix = `#/${defsSegment}/`;
		const defId = entry[1].schema.id ?? `__schema${ctx.counter++}`;
		return {
			defId,
			ref: defUriPrefix + defId
		};
	};
	const extractToDef = (entry) => {
		if (entry[1].schema.$ref) return;
		const seen = entry[1];
		const { ref, defId } = makeURI(entry);
		seen.def = { ...seen.schema };
		if (defId) seen.defId = defId;
		const schema = seen.schema;
		for (const key in schema) delete schema[key];
		schema.$ref = ref;
	};
	if (ctx.cycles === "throw") for (const entry of ctx.seen.entries()) {
		const seen = entry[1];
		if (seen.cycle) throw new Error(`Cycle detected: #/${seen.cycle?.join("/")}/<root>

Set the \`cycles\` parameter to \`"ref"\` to resolve cyclical schemas with defs.`);
	}
	for (const entry of ctx.seen.entries()) {
		const seen = entry[1];
		if (schema === entry[0]) {
			extractToDef(entry);
			continue;
		}
		if (ctx.external) {
			const ext = ctx.external.registry.get(entry[0])?.id;
			if (schema !== entry[0] && ext) {
				extractToDef(entry);
				continue;
			}
		}
		if (ctx.metadataRegistry.get(entry[0])?.id) {
			extractToDef(entry);
			continue;
		}
		if (seen.cycle) {
			extractToDef(entry);
			continue;
		}
		if (seen.count > 1) {
			if (ctx.reused === "ref") {
				extractToDef(entry);
				continue;
			}
		}
	}
}
function finalize(ctx, schema) {
	const root = ctx.seen.get(schema);
	if (!root) throw new Error("Unprocessed schema. This is a bug in Zod.");
	const flattenRef = (zodSchema) => {
		const seen = ctx.seen.get(zodSchema);
		if (seen.ref === null) return;
		const schema = seen.def ?? seen.schema;
		const _cached = { ...schema };
		const ref = seen.ref;
		seen.ref = null;
		if (ref) {
			flattenRef(ref);
			const refSeen = ctx.seen.get(ref);
			const refSchema = refSeen.schema;
			if (refSchema.$ref && (ctx.target === "draft-07" || ctx.target === "draft-04" || ctx.target === "openapi-3.0")) {
				schema.allOf = schema.allOf ?? [];
				schema.allOf.push(refSchema);
			} else Object.assign(schema, refSchema);
			Object.assign(schema, _cached);
			if (zodSchema._zod.parent === ref) for (const key in schema) {
				if (key === "$ref" || key === "allOf") continue;
				if (!(key in _cached)) delete schema[key];
			}
			if (refSchema.$ref && refSeen.def) for (const key in schema) {
				if (key === "$ref" || key === "allOf") continue;
				if (key in refSeen.def && JSON.stringify(schema[key]) === JSON.stringify(refSeen.def[key])) delete schema[key];
			}
		}
		const parent = zodSchema._zod.parent;
		if (parent && parent !== ref) {
			flattenRef(parent);
			const parentSeen = ctx.seen.get(parent);
			if (parentSeen?.schema.$ref) {
				schema.$ref = parentSeen.schema.$ref;
				if (parentSeen.def) for (const key in schema) {
					if (key === "$ref" || key === "allOf") continue;
					if (key in parentSeen.def && JSON.stringify(schema[key]) === JSON.stringify(parentSeen.def[key])) delete schema[key];
				}
			}
		}
		ctx.override({
			zodSchema,
			jsonSchema: schema,
			path: seen.path ?? []
		});
	};
	for (const entry of [...ctx.seen.entries()].reverse()) flattenRef(entry[0]);
	const result = {};
	if (ctx.target === "draft-2020-12") result.$schema = "https://json-schema.org/draft/2020-12/schema";
	else if (ctx.target === "draft-07") result.$schema = "http://json-schema.org/draft-07/schema#";
	else if (ctx.target === "draft-04") result.$schema = "http://json-schema.org/draft-04/schema#";
	else if (ctx.target === "openapi-3.0") {}
	if (ctx.external?.uri) {
		const id = ctx.external.registry.get(schema)?.id;
		if (!id) throw new Error("Schema is missing an `id` property");
		result.$id = ctx.external.uri(id);
	}
	Object.assign(result, root.def ?? root.schema);
	const rootMetaId = ctx.metadataRegistry.get(schema)?.id;
	if (rootMetaId !== void 0 && result.id === rootMetaId) delete result.id;
	const defs = ctx.external?.defs ?? {};
	for (const entry of ctx.seen.entries()) {
		const seen = entry[1];
		if (seen.def && seen.defId) {
			if (seen.def.id === seen.defId) delete seen.def.id;
			defs[seen.defId] = seen.def;
		}
	}
	if (ctx.external) {} else if (Object.keys(defs).length > 0) if (ctx.target === "draft-2020-12") result.$defs = defs;
	else result.definitions = defs;
	try {
		const finalized = JSON.parse(JSON.stringify(result));
		Object.defineProperty(finalized, "~standard", {
			value: {
				...schema["~standard"],
				jsonSchema: {
					input: createStandardJSONSchemaMethod(schema, "input", ctx.processors),
					output: createStandardJSONSchemaMethod(schema, "output", ctx.processors)
				}
			},
			enumerable: false,
			writable: false
		});
		return finalized;
	} catch (_err) {
		throw new Error("Error converting schema to JSON.");
	}
}
function isTransforming(_schema, _ctx) {
	const ctx = _ctx ?? { seen: /* @__PURE__ */ new Set() };
	if (ctx.seen.has(_schema)) return false;
	ctx.seen.add(_schema);
	const def = _schema._zod.def;
	if (def.type === "transform") return true;
	if (def.type === "array") return isTransforming(def.element, ctx);
	if (def.type === "set") return isTransforming(def.valueType, ctx);
	if (def.type === "lazy") return isTransforming(def.getter(), ctx);
	if (def.type === "promise" || def.type === "optional" || def.type === "nonoptional" || def.type === "nullable" || def.type === "readonly" || def.type === "default" || def.type === "prefault") return isTransforming(def.innerType, ctx);
	if (def.type === "intersection") return isTransforming(def.left, ctx) || isTransforming(def.right, ctx);
	if (def.type === "record" || def.type === "map") return isTransforming(def.keyType, ctx) || isTransforming(def.valueType, ctx);
	if (def.type === "pipe") {
		if (_schema._zod.traits.has("$ZodCodec")) return true;
		return isTransforming(def.in, ctx) || isTransforming(def.out, ctx);
	}
	if (def.type === "object") {
		for (const key in def.shape) if (isTransforming(def.shape[key], ctx)) return true;
		return false;
	}
	if (def.type === "union") {
		for (const option of def.options) if (isTransforming(option, ctx)) return true;
		return false;
	}
	if (def.type === "tuple") {
		for (const item of def.items) if (isTransforming(item, ctx)) return true;
		if (def.rest && isTransforming(def.rest, ctx)) return true;
		return false;
	}
	return false;
}
/**
* Creates a toJSONSchema method for a schema instance.
* This encapsulates the logic of initializing context, processing, extracting defs, and finalizing.
*/
const createToJSONSchemaMethod = (schema, processors = {}) => (params) => {
	const ctx = initializeContext({
		...params,
		processors
	});
	process(schema, ctx);
	extractDefs(ctx, schema);
	return finalize(ctx, schema);
};
const createStandardJSONSchemaMethod = (schema, io, processors = {}) => (params) => {
	const { libraryOptions, target } = params ?? {};
	const ctx = initializeContext({
		...libraryOptions ?? {},
		target,
		io,
		processors
	});
	process(schema, ctx);
	extractDefs(ctx, schema);
	return finalize(ctx, schema);
};
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/core/json-schema-processors.js
const formatMap = {
	guid: "uuid",
	url: "uri",
	datetime: "date-time",
	json_string: "json-string",
	regex: ""
};
const stringProcessor = (schema, ctx, _json, _params) => {
	const json = _json;
	json.type = "string";
	const { minimum, maximum, format, patterns, contentEncoding } = schema._zod.bag;
	if (typeof minimum === "number") json.minLength = minimum;
	if (typeof maximum === "number") json.maxLength = maximum;
	if (format) {
		json.format = formatMap[format] ?? format;
		if (json.format === "") delete json.format;
		if (format === "time") delete json.format;
	}
	if (contentEncoding) json.contentEncoding = contentEncoding;
	if (patterns && patterns.size > 0) {
		const regexes = [...patterns];
		if (regexes.length === 1) json.pattern = regexes[0].source;
		else if (regexes.length > 1) json.allOf = [...regexes.map((regex) => ({
			...ctx.target === "draft-07" || ctx.target === "draft-04" || ctx.target === "openapi-3.0" ? { type: "string" } : {},
			pattern: regex.source
		}))];
	}
};
const numberProcessor = (schema, ctx, _json, _params) => {
	const json = _json;
	const { minimum, maximum, format, multipleOf, exclusiveMaximum, exclusiveMinimum } = schema._zod.bag;
	if (typeof format === "string" && format.includes("int")) json.type = "integer";
	else json.type = "number";
	const exMin = typeof exclusiveMinimum === "number" && exclusiveMinimum >= (minimum ?? Number.NEGATIVE_INFINITY);
	const exMax = typeof exclusiveMaximum === "number" && exclusiveMaximum <= (maximum ?? Number.POSITIVE_INFINITY);
	const legacy = ctx.target === "draft-04" || ctx.target === "openapi-3.0";
	if (exMin) if (legacy) {
		json.minimum = exclusiveMinimum;
		json.exclusiveMinimum = true;
	} else json.exclusiveMinimum = exclusiveMinimum;
	else if (typeof minimum === "number") json.minimum = minimum;
	if (exMax) if (legacy) {
		json.maximum = exclusiveMaximum;
		json.exclusiveMaximum = true;
	} else json.exclusiveMaximum = exclusiveMaximum;
	else if (typeof maximum === "number") json.maximum = maximum;
	if (typeof multipleOf === "number") json.multipleOf = multipleOf;
};
const booleanProcessor = (_schema, _ctx, json, _params) => {
	json.type = "boolean";
};
const neverProcessor = (_schema, _ctx, json, _params) => {
	json.not = {};
};
const enumProcessor = (schema, _ctx, json, _params) => {
	const def = schema._zod.def;
	const values = getEnumValues(def.entries);
	if (values.every((v) => typeof v === "number")) json.type = "number";
	if (values.every((v) => typeof v === "string")) json.type = "string";
	json.enum = values;
};
const literalProcessor = (schema, ctx, json, _params) => {
	const def = schema._zod.def;
	const vals = [];
	for (const val of def.values) if (val === void 0) {
		if (ctx.unrepresentable === "throw") throw new Error("Literal `undefined` cannot be represented in JSON Schema");
	} else if (typeof val === "bigint") if (ctx.unrepresentable === "throw") throw new Error("BigInt literals cannot be represented in JSON Schema");
	else vals.push(Number(val));
	else vals.push(val);
	if (vals.length === 0) {} else if (vals.length === 1) {
		const val = vals[0];
		json.type = val === null ? "null" : typeof val;
		if (ctx.target === "draft-04" || ctx.target === "openapi-3.0") json.enum = [val];
		else json.const = val;
	} else {
		if (vals.every((v) => typeof v === "number")) json.type = "number";
		if (vals.every((v) => typeof v === "string")) json.type = "string";
		if (vals.every((v) => typeof v === "boolean")) json.type = "boolean";
		if (vals.every((v) => v === null)) json.type = "null";
		json.enum = vals;
	}
};
const customProcessor = (_schema, ctx, _json, _params) => {
	if (ctx.unrepresentable === "throw") throw new Error("Custom types cannot be represented in JSON Schema");
};
const transformProcessor = (_schema, ctx, _json, _params) => {
	if (ctx.unrepresentable === "throw") throw new Error("Transforms cannot be represented in JSON Schema");
};
const arrayProcessor = (schema, ctx, _json, params) => {
	const json = _json;
	const def = schema._zod.def;
	const { minimum, maximum } = schema._zod.bag;
	if (typeof minimum === "number") json.minItems = minimum;
	if (typeof maximum === "number") json.maxItems = maximum;
	json.type = "array";
	json.items = process(def.element, ctx, {
		...params,
		path: [...params.path, "items"]
	});
};
const objectProcessor = (schema, ctx, _json, params) => {
	const json = _json;
	const def = schema._zod.def;
	json.type = "object";
	json.properties = {};
	const shape = def.shape;
	for (const key in shape) json.properties[key] = process(shape[key], ctx, {
		...params,
		path: [
			...params.path,
			"properties",
			key
		]
	});
	const allKeys = new Set(Object.keys(shape));
	const requiredKeys = new Set([...allKeys].filter((key) => {
		const v = def.shape[key]._zod;
		if (ctx.io === "input") return v.optin === void 0;
		else return v.optout === void 0;
	}));
	if (requiredKeys.size > 0) json.required = Array.from(requiredKeys);
	if (def.catchall?._zod.def.type === "never") json.additionalProperties = false;
	else if (!def.catchall) {
		if (ctx.io === "output") json.additionalProperties = false;
	} else if (def.catchall) json.additionalProperties = process(def.catchall, ctx, {
		...params,
		path: [...params.path, "additionalProperties"]
	});
};
const unionProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	const isExclusive = def.inclusive === false;
	const options = def.options.map((x, i) => process(x, ctx, {
		...params,
		path: [
			...params.path,
			isExclusive ? "oneOf" : "anyOf",
			i
		]
	}));
	if (isExclusive) json.oneOf = options;
	else json.anyOf = options;
};
const intersectionProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	const a = process(def.left, ctx, {
		...params,
		path: [
			...params.path,
			"allOf",
			0
		]
	});
	const b = process(def.right, ctx, {
		...params,
		path: [
			...params.path,
			"allOf",
			1
		]
	});
	const isSimpleIntersection = (val) => "allOf" in val && Object.keys(val).length === 1;
	json.allOf = [...isSimpleIntersection(a) ? a.allOf : [a], ...isSimpleIntersection(b) ? b.allOf : [b]];
};
const nullableProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	const inner = process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	if (ctx.target === "openapi-3.0") {
		seen.ref = def.innerType;
		json.nullable = true;
	} else json.anyOf = [inner, { type: "null" }];
};
const nonoptionalProcessor = (schema, ctx, _json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
};
const defaultProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
	json.default = JSON.parse(JSON.stringify(def.defaultValue));
};
const prefaultProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
	if (ctx.io === "input") json._prefault = JSON.parse(JSON.stringify(def.defaultValue));
};
const catchProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
	let catchValue;
	try {
		catchValue = def.catchValue(void 0);
	} catch {
		throw new Error("Dynamic catch values are not supported in JSON Schema");
	}
	json.default = catchValue;
};
const pipeProcessor = (schema, ctx, _json, params) => {
	const def = schema._zod.def;
	const inIsTransform = def.in._zod.traits.has("$ZodTransform");
	const innerType = ctx.io === "input" ? inIsTransform ? def.out : def.in : def.out;
	process(innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = innerType;
};
const readonlyProcessor = (schema, ctx, json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
	json.readOnly = true;
};
const optionalProcessor = (schema, ctx, _json, params) => {
	const def = schema._zod.def;
	process(def.innerType, ctx, params);
	const seen = ctx.seen.get(schema);
	seen.ref = def.innerType;
};
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/classic/iso.js
const ZodISODateTime = /*@__PURE__*/ $constructor("ZodISODateTime", (inst, def) => {
	$ZodISODateTime.init(inst, def);
	ZodStringFormat.init(inst, def);
});
function datetime(params) {
	return /* @__PURE__ */ _isoDateTime(ZodISODateTime, params);
}
const ZodISODate = /*@__PURE__*/ $constructor("ZodISODate", (inst, def) => {
	$ZodISODate.init(inst, def);
	ZodStringFormat.init(inst, def);
});
function date(params) {
	return /* @__PURE__ */ _isoDate(ZodISODate, params);
}
const ZodISOTime = /*@__PURE__*/ $constructor("ZodISOTime", (inst, def) => {
	$ZodISOTime.init(inst, def);
	ZodStringFormat.init(inst, def);
});
function time(params) {
	return /* @__PURE__ */ _isoTime(ZodISOTime, params);
}
const ZodISODuration = /*@__PURE__*/ $constructor("ZodISODuration", (inst, def) => {
	$ZodISODuration.init(inst, def);
	ZodStringFormat.init(inst, def);
});
function duration(params) {
	return /* @__PURE__ */ _isoDuration(ZodISODuration, params);
}
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/classic/errors.js
const initializer = (inst, issues) => {
	$ZodError.init(inst, issues);
	inst.name = "ZodError";
	Object.defineProperties(inst, {
		format: { value: (mapper) => formatError(inst, mapper) },
		flatten: { value: (mapper) => flattenError(inst, mapper) },
		addIssue: { value: (issue) => {
			inst.issues.push(issue);
			inst.message = JSON.stringify(inst.issues, jsonStringifyReplacer, 2);
		} },
		addIssues: { value: (issues) => {
			inst.issues.push(...issues);
			inst.message = JSON.stringify(inst.issues, jsonStringifyReplacer, 2);
		} },
		isEmpty: { get() {
			return inst.issues.length === 0;
		} }
	});
};
const ZodRealError = /*@__PURE__*/ $constructor("ZodError", initializer, { Parent: Error });
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/classic/parse.js
const parse = /* @__PURE__ */ _parse(ZodRealError);
const parseAsync = /* @__PURE__ */ _parseAsync(ZodRealError);
const safeParse = /* @__PURE__ */ _safeParse(ZodRealError);
const safeParseAsync = /* @__PURE__ */ _safeParseAsync(ZodRealError);
const encode = /* @__PURE__ */ _encode(ZodRealError);
const decode = /* @__PURE__ */ _decode(ZodRealError);
const encodeAsync = /* @__PURE__ */ _encodeAsync(ZodRealError);
const decodeAsync = /* @__PURE__ */ _decodeAsync(ZodRealError);
const safeEncode = /* @__PURE__ */ _safeEncode(ZodRealError);
const safeDecode = /* @__PURE__ */ _safeDecode(ZodRealError);
const safeEncodeAsync = /* @__PURE__ */ _safeEncodeAsync(ZodRealError);
const safeDecodeAsync = /* @__PURE__ */ _safeDecodeAsync(ZodRealError);
//#endregion
//#region ../../node_modules/.pnpm/zod@4.4.3/node_modules/zod/v4/classic/schemas.js
const _installedGroups = /* @__PURE__ */ new WeakMap();
function _installLazyMethods(inst, group, methods) {
	const proto = Object.getPrototypeOf(inst);
	let installed = _installedGroups.get(proto);
	if (!installed) {
		installed = /* @__PURE__ */ new Set();
		_installedGroups.set(proto, installed);
	}
	if (installed.has(group)) return;
	installed.add(group);
	for (const key in methods) {
		const fn = methods[key];
		Object.defineProperty(proto, key, {
			configurable: true,
			enumerable: false,
			get() {
				const bound = fn.bind(this);
				Object.defineProperty(this, key, {
					configurable: true,
					writable: true,
					enumerable: true,
					value: bound
				});
				return bound;
			},
			set(v) {
				Object.defineProperty(this, key, {
					configurable: true,
					writable: true,
					enumerable: true,
					value: v
				});
			}
		});
	}
}
const ZodType = /*@__PURE__*/ $constructor("ZodType", (inst, def) => {
	$ZodType.init(inst, def);
	Object.assign(inst["~standard"], { jsonSchema: {
		input: createStandardJSONSchemaMethod(inst, "input"),
		output: createStandardJSONSchemaMethod(inst, "output")
	} });
	inst.toJSONSchema = createToJSONSchemaMethod(inst, {});
	inst.def = def;
	inst.type = def.type;
	Object.defineProperty(inst, "_def", { value: def });
	inst.parse = (data, params) => parse(inst, data, params, { callee: inst.parse });
	inst.safeParse = (data, params) => safeParse(inst, data, params);
	inst.parseAsync = async (data, params) => parseAsync(inst, data, params, { callee: inst.parseAsync });
	inst.safeParseAsync = async (data, params) => safeParseAsync(inst, data, params);
	inst.spa = inst.safeParseAsync;
	inst.encode = (data, params) => encode(inst, data, params);
	inst.decode = (data, params) => decode(inst, data, params);
	inst.encodeAsync = async (data, params) => encodeAsync(inst, data, params);
	inst.decodeAsync = async (data, params) => decodeAsync(inst, data, params);
	inst.safeEncode = (data, params) => safeEncode(inst, data, params);
	inst.safeDecode = (data, params) => safeDecode(inst, data, params);
	inst.safeEncodeAsync = async (data, params) => safeEncodeAsync(inst, data, params);
	inst.safeDecodeAsync = async (data, params) => safeDecodeAsync(inst, data, params);
	_installLazyMethods(inst, "ZodType", {
		check(...chks) {
			const def = this.def;
			return this.clone(mergeDefs(def, { checks: [...def.checks ?? [], ...chks.map((ch) => typeof ch === "function" ? { _zod: {
				check: ch,
				def: { check: "custom" },
				onattach: []
			} } : ch)] }), { parent: true });
		},
		with(...chks) {
			return this.check(...chks);
		},
		clone(def, params) {
			return clone(this, def, params);
		},
		brand() {
			return this;
		},
		register(reg, meta) {
			reg.add(this, meta);
			return this;
		},
		refine(check, params) {
			return this.check(refine(check, params));
		},
		superRefine(refinement, params) {
			return this.check(superRefine(refinement, params));
		},
		overwrite(fn) {
			return this.check(/* @__PURE__ */ _overwrite(fn));
		},
		optional() {
			return optional(this);
		},
		exactOptional() {
			return exactOptional(this);
		},
		nullable() {
			return nullable(this);
		},
		nullish() {
			return optional(nullable(this));
		},
		nonoptional(params) {
			return nonoptional(this, params);
		},
		array() {
			return array(this);
		},
		or(arg) {
			return union([this, arg]);
		},
		and(arg) {
			return intersection(this, arg);
		},
		transform(tx) {
			return pipe(this, transform(tx));
		},
		default(d) {
			return _default(this, d);
		},
		prefault(d) {
			return prefault(this, d);
		},
		catch(params) {
			return _catch(this, params);
		},
		pipe(target) {
			return pipe(this, target);
		},
		readonly() {
			return readonly(this);
		},
		describe(description) {
			const cl = this.clone();
			globalRegistry.add(cl, { description });
			return cl;
		},
		meta(...args) {
			if (args.length === 0) return globalRegistry.get(this);
			const cl = this.clone();
			globalRegistry.add(cl, args[0]);
			return cl;
		},
		isOptional() {
			return this.safeParse(void 0).success;
		},
		isNullable() {
			return this.safeParse(null).success;
		},
		apply(fn) {
			return fn(this);
		}
	});
	Object.defineProperty(inst, "description", {
		get() {
			return globalRegistry.get(inst)?.description;
		},
		configurable: true
	});
	return inst;
});
/** @internal */
const _ZodString = /*@__PURE__*/ $constructor("_ZodString", (inst, def) => {
	$ZodString.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => stringProcessor(inst, ctx, json, params);
	const bag = inst._zod.bag;
	inst.format = bag.format ?? null;
	inst.minLength = bag.minimum ?? null;
	inst.maxLength = bag.maximum ?? null;
	_installLazyMethods(inst, "_ZodString", {
		regex(...args) {
			return this.check(/* @__PURE__ */ _regex(...args));
		},
		includes(...args) {
			return this.check(/* @__PURE__ */ _includes(...args));
		},
		startsWith(...args) {
			return this.check(/* @__PURE__ */ _startsWith(...args));
		},
		endsWith(...args) {
			return this.check(/* @__PURE__ */ _endsWith(...args));
		},
		min(...args) {
			return this.check(/* @__PURE__ */ _minLength(...args));
		},
		max(...args) {
			return this.check(/* @__PURE__ */ _maxLength(...args));
		},
		length(...args) {
			return this.check(/* @__PURE__ */ _length(...args));
		},
		nonempty(...args) {
			return this.check(/* @__PURE__ */ _minLength(1, ...args));
		},
		lowercase(params) {
			return this.check(/* @__PURE__ */ _lowercase(params));
		},
		uppercase(params) {
			return this.check(/* @__PURE__ */ _uppercase(params));
		},
		trim() {
			return this.check(/* @__PURE__ */ _trim());
		},
		normalize(...args) {
			return this.check(/* @__PURE__ */ _normalize(...args));
		},
		toLowerCase() {
			return this.check(/* @__PURE__ */ _toLowerCase());
		},
		toUpperCase() {
			return this.check(/* @__PURE__ */ _toUpperCase());
		},
		slugify() {
			return this.check(/* @__PURE__ */ _slugify());
		}
	});
});
const ZodString = /*@__PURE__*/ $constructor("ZodString", (inst, def) => {
	$ZodString.init(inst, def);
	_ZodString.init(inst, def);
	inst.email = (params) => inst.check(/* @__PURE__ */ _email(ZodEmail, params));
	inst.url = (params) => inst.check(/* @__PURE__ */ _url(ZodURL, params));
	inst.jwt = (params) => inst.check(/* @__PURE__ */ _jwt(ZodJWT, params));
	inst.emoji = (params) => inst.check(/* @__PURE__ */ _emoji(ZodEmoji, params));
	inst.guid = (params) => inst.check(/* @__PURE__ */ _guid(ZodGUID, params));
	inst.uuid = (params) => inst.check(/* @__PURE__ */ _uuid(ZodUUID, params));
	inst.uuidv4 = (params) => inst.check(/* @__PURE__ */ _uuidv4(ZodUUID, params));
	inst.uuidv6 = (params) => inst.check(/* @__PURE__ */ _uuidv6(ZodUUID, params));
	inst.uuidv7 = (params) => inst.check(/* @__PURE__ */ _uuidv7(ZodUUID, params));
	inst.nanoid = (params) => inst.check(/* @__PURE__ */ _nanoid(ZodNanoID, params));
	inst.guid = (params) => inst.check(/* @__PURE__ */ _guid(ZodGUID, params));
	inst.cuid = (params) => inst.check(/* @__PURE__ */ _cuid(ZodCUID, params));
	inst.cuid2 = (params) => inst.check(/* @__PURE__ */ _cuid2(ZodCUID2, params));
	inst.ulid = (params) => inst.check(/* @__PURE__ */ _ulid(ZodULID, params));
	inst.base64 = (params) => inst.check(/* @__PURE__ */ _base64(ZodBase64, params));
	inst.base64url = (params) => inst.check(/* @__PURE__ */ _base64url(ZodBase64URL, params));
	inst.xid = (params) => inst.check(/* @__PURE__ */ _xid(ZodXID, params));
	inst.ksuid = (params) => inst.check(/* @__PURE__ */ _ksuid(ZodKSUID, params));
	inst.ipv4 = (params) => inst.check(/* @__PURE__ */ _ipv4(ZodIPv4, params));
	inst.ipv6 = (params) => inst.check(/* @__PURE__ */ _ipv6(ZodIPv6, params));
	inst.cidrv4 = (params) => inst.check(/* @__PURE__ */ _cidrv4(ZodCIDRv4, params));
	inst.cidrv6 = (params) => inst.check(/* @__PURE__ */ _cidrv6(ZodCIDRv6, params));
	inst.e164 = (params) => inst.check(/* @__PURE__ */ _e164(ZodE164, params));
	inst.datetime = (params) => inst.check(datetime(params));
	inst.date = (params) => inst.check(date(params));
	inst.time = (params) => inst.check(time(params));
	inst.duration = (params) => inst.check(duration(params));
});
function string(params) {
	return /* @__PURE__ */ _string(ZodString, params);
}
const ZodStringFormat = /*@__PURE__*/ $constructor("ZodStringFormat", (inst, def) => {
	$ZodStringFormat.init(inst, def);
	_ZodString.init(inst, def);
});
const ZodEmail = /*@__PURE__*/ $constructor("ZodEmail", (inst, def) => {
	$ZodEmail.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodGUID = /*@__PURE__*/ $constructor("ZodGUID", (inst, def) => {
	$ZodGUID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodUUID = /*@__PURE__*/ $constructor("ZodUUID", (inst, def) => {
	$ZodUUID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodURL = /*@__PURE__*/ $constructor("ZodURL", (inst, def) => {
	$ZodURL.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodEmoji = /*@__PURE__*/ $constructor("ZodEmoji", (inst, def) => {
	$ZodEmoji.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodNanoID = /*@__PURE__*/ $constructor("ZodNanoID", (inst, def) => {
	$ZodNanoID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
/**
* @deprecated CUID v1 is deprecated by its authors due to information leakage
* (timestamps embedded in the id). Use {@link ZodCUID2} instead.
* See https://github.com/paralleldrive/cuid.
*/
const ZodCUID = /*@__PURE__*/ $constructor("ZodCUID", (inst, def) => {
	$ZodCUID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodCUID2 = /*@__PURE__*/ $constructor("ZodCUID2", (inst, def) => {
	$ZodCUID2.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodULID = /*@__PURE__*/ $constructor("ZodULID", (inst, def) => {
	$ZodULID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodXID = /*@__PURE__*/ $constructor("ZodXID", (inst, def) => {
	$ZodXID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodKSUID = /*@__PURE__*/ $constructor("ZodKSUID", (inst, def) => {
	$ZodKSUID.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodIPv4 = /*@__PURE__*/ $constructor("ZodIPv4", (inst, def) => {
	$ZodIPv4.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodIPv6 = /*@__PURE__*/ $constructor("ZodIPv6", (inst, def) => {
	$ZodIPv6.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodCIDRv4 = /*@__PURE__*/ $constructor("ZodCIDRv4", (inst, def) => {
	$ZodCIDRv4.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodCIDRv6 = /*@__PURE__*/ $constructor("ZodCIDRv6", (inst, def) => {
	$ZodCIDRv6.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodBase64 = /*@__PURE__*/ $constructor("ZodBase64", (inst, def) => {
	$ZodBase64.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodBase64URL = /*@__PURE__*/ $constructor("ZodBase64URL", (inst, def) => {
	$ZodBase64URL.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodE164 = /*@__PURE__*/ $constructor("ZodE164", (inst, def) => {
	$ZodE164.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodJWT = /*@__PURE__*/ $constructor("ZodJWT", (inst, def) => {
	$ZodJWT.init(inst, def);
	ZodStringFormat.init(inst, def);
});
const ZodNumber = /*@__PURE__*/ $constructor("ZodNumber", (inst, def) => {
	$ZodNumber.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => numberProcessor(inst, ctx, json, params);
	_installLazyMethods(inst, "ZodNumber", {
		gt(value, params) {
			return this.check(/* @__PURE__ */ _gt(value, params));
		},
		gte(value, params) {
			return this.check(/* @__PURE__ */ _gte(value, params));
		},
		min(value, params) {
			return this.check(/* @__PURE__ */ _gte(value, params));
		},
		lt(value, params) {
			return this.check(/* @__PURE__ */ _lt(value, params));
		},
		lte(value, params) {
			return this.check(/* @__PURE__ */ _lte(value, params));
		},
		max(value, params) {
			return this.check(/* @__PURE__ */ _lte(value, params));
		},
		int(params) {
			return this.check(int(params));
		},
		safe(params) {
			return this.check(int(params));
		},
		positive(params) {
			return this.check(/* @__PURE__ */ _gt(0, params));
		},
		nonnegative(params) {
			return this.check(/* @__PURE__ */ _gte(0, params));
		},
		negative(params) {
			return this.check(/* @__PURE__ */ _lt(0, params));
		},
		nonpositive(params) {
			return this.check(/* @__PURE__ */ _lte(0, params));
		},
		multipleOf(value, params) {
			return this.check(/* @__PURE__ */ _multipleOf(value, params));
		},
		step(value, params) {
			return this.check(/* @__PURE__ */ _multipleOf(value, params));
		},
		finite() {
			return this;
		}
	});
	const bag = inst._zod.bag;
	inst.minValue = Math.max(bag.minimum ?? Number.NEGATIVE_INFINITY, bag.exclusiveMinimum ?? Number.NEGATIVE_INFINITY) ?? null;
	inst.maxValue = Math.min(bag.maximum ?? Number.POSITIVE_INFINITY, bag.exclusiveMaximum ?? Number.POSITIVE_INFINITY) ?? null;
	inst.isInt = (bag.format ?? "").includes("int") || Number.isSafeInteger(bag.multipleOf ?? .5);
	inst.isFinite = true;
	inst.format = bag.format ?? null;
});
function number(params) {
	return /* @__PURE__ */ _number(ZodNumber, params);
}
const ZodNumberFormat = /*@__PURE__*/ $constructor("ZodNumberFormat", (inst, def) => {
	$ZodNumberFormat.init(inst, def);
	ZodNumber.init(inst, def);
});
function int(params) {
	return /* @__PURE__ */ _int(ZodNumberFormat, params);
}
const ZodBoolean = /*@__PURE__*/ $constructor("ZodBoolean", (inst, def) => {
	$ZodBoolean.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => booleanProcessor(inst, ctx, json, params);
});
function boolean(params) {
	return /* @__PURE__ */ _boolean(ZodBoolean, params);
}
const ZodUnknown = /*@__PURE__*/ $constructor("ZodUnknown", (inst, def) => {
	$ZodUnknown.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => void 0;
});
function unknown() {
	return /* @__PURE__ */ _unknown(ZodUnknown);
}
const ZodNever = /*@__PURE__*/ $constructor("ZodNever", (inst, def) => {
	$ZodNever.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => neverProcessor(inst, ctx, json, params);
});
function never(params) {
	return /* @__PURE__ */ _never(ZodNever, params);
}
const ZodArray = /*@__PURE__*/ $constructor("ZodArray", (inst, def) => {
	$ZodArray.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => arrayProcessor(inst, ctx, json, params);
	inst.element = def.element;
	_installLazyMethods(inst, "ZodArray", {
		min(n, params) {
			return this.check(/* @__PURE__ */ _minLength(n, params));
		},
		nonempty(params) {
			return this.check(/* @__PURE__ */ _minLength(1, params));
		},
		max(n, params) {
			return this.check(/* @__PURE__ */ _maxLength(n, params));
		},
		length(n, params) {
			return this.check(/* @__PURE__ */ _length(n, params));
		},
		unwrap() {
			return this.element;
		}
	});
});
function array(element, params) {
	return /* @__PURE__ */ _array(ZodArray, element, params);
}
const ZodObject = /*@__PURE__*/ $constructor("ZodObject", (inst, def) => {
	$ZodObjectJIT.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => objectProcessor(inst, ctx, json, params);
	defineLazy(inst, "shape", () => {
		return def.shape;
	});
	_installLazyMethods(inst, "ZodObject", {
		keyof() {
			return _enum(Object.keys(this._zod.def.shape));
		},
		catchall(catchall) {
			return this.clone({
				...this._zod.def,
				catchall
			});
		},
		passthrough() {
			return this.clone({
				...this._zod.def,
				catchall: unknown()
			});
		},
		loose() {
			return this.clone({
				...this._zod.def,
				catchall: unknown()
			});
		},
		strict() {
			return this.clone({
				...this._zod.def,
				catchall: never()
			});
		},
		strip() {
			return this.clone({
				...this._zod.def,
				catchall: void 0
			});
		},
		extend(incoming) {
			return extend(this, incoming);
		},
		safeExtend(incoming) {
			return safeExtend(this, incoming);
		},
		merge(other) {
			return merge(this, other);
		},
		pick(mask) {
			return pick(this, mask);
		},
		omit(mask) {
			return omit(this, mask);
		},
		partial(...args) {
			return partial(ZodOptional, this, args[0]);
		},
		required(...args) {
			return required(ZodNonOptional, this, args[0]);
		}
	});
});
function object(shape, params) {
	return new ZodObject({
		type: "object",
		shape: shape ?? {},
		...normalizeParams(params)
	});
}
const ZodUnion = /*@__PURE__*/ $constructor("ZodUnion", (inst, def) => {
	$ZodUnion.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => unionProcessor(inst, ctx, json, params);
	inst.options = def.options;
});
function union(options, params) {
	return new ZodUnion({
		type: "union",
		options,
		...normalizeParams(params)
	});
}
const ZodIntersection = /*@__PURE__*/ $constructor("ZodIntersection", (inst, def) => {
	$ZodIntersection.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => intersectionProcessor(inst, ctx, json, params);
});
function intersection(left, right) {
	return new ZodIntersection({
		type: "intersection",
		left,
		right
	});
}
const ZodEnum = /*@__PURE__*/ $constructor("ZodEnum", (inst, def) => {
	$ZodEnum.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => enumProcessor(inst, ctx, json, params);
	inst.enum = def.entries;
	inst.options = Object.values(def.entries);
	const keys = new Set(Object.keys(def.entries));
	inst.extract = (values, params) => {
		const newEntries = {};
		for (const value of values) if (keys.has(value)) newEntries[value] = def.entries[value];
		else throw new Error(`Key ${value} not found in enum`);
		return new ZodEnum({
			...def,
			checks: [],
			...normalizeParams(params),
			entries: newEntries
		});
	};
	inst.exclude = (values, params) => {
		const newEntries = { ...def.entries };
		for (const value of values) if (keys.has(value)) delete newEntries[value];
		else throw new Error(`Key ${value} not found in enum`);
		return new ZodEnum({
			...def,
			checks: [],
			...normalizeParams(params),
			entries: newEntries
		});
	};
});
function _enum(values, params) {
	return new ZodEnum({
		type: "enum",
		entries: Array.isArray(values) ? Object.fromEntries(values.map((v) => [v, v])) : values,
		...normalizeParams(params)
	});
}
const ZodLiteral = /*@__PURE__*/ $constructor("ZodLiteral", (inst, def) => {
	$ZodLiteral.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => literalProcessor(inst, ctx, json, params);
	inst.values = new Set(def.values);
	Object.defineProperty(inst, "value", { get() {
		if (def.values.length > 1) throw new Error("This schema contains multiple valid literal values. Use `.values` instead.");
		return def.values[0];
	} });
});
function literal(value, params) {
	return new ZodLiteral({
		type: "literal",
		values: Array.isArray(value) ? value : [value],
		...normalizeParams(params)
	});
}
const ZodTransform = /*@__PURE__*/ $constructor("ZodTransform", (inst, def) => {
	$ZodTransform.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => transformProcessor(inst, ctx, json, params);
	inst._zod.parse = (payload, _ctx) => {
		if (_ctx.direction === "backward") throw new $ZodEncodeError(inst.constructor.name);
		payload.addIssue = (issue$1) => {
			if (typeof issue$1 === "string") payload.issues.push(issue(issue$1, payload.value, def));
			else {
				const _issue = issue$1;
				if (_issue.fatal) _issue.continue = false;
				_issue.code ?? (_issue.code = "custom");
				_issue.input ?? (_issue.input = payload.value);
				_issue.inst ?? (_issue.inst = inst);
				payload.issues.push(issue(_issue));
			}
		};
		const output = def.transform(payload.value, payload);
		if (output instanceof Promise) return output.then((output) => {
			payload.value = output;
			payload.fallback = true;
			return payload;
		});
		payload.value = output;
		payload.fallback = true;
		return payload;
	};
});
function transform(fn) {
	return new ZodTransform({
		type: "transform",
		transform: fn
	});
}
const ZodOptional = /*@__PURE__*/ $constructor("ZodOptional", (inst, def) => {
	$ZodOptional.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => optionalProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function optional(innerType) {
	return new ZodOptional({
		type: "optional",
		innerType
	});
}
const ZodExactOptional = /*@__PURE__*/ $constructor("ZodExactOptional", (inst, def) => {
	$ZodExactOptional.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => optionalProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function exactOptional(innerType) {
	return new ZodExactOptional({
		type: "optional",
		innerType
	});
}
const ZodNullable = /*@__PURE__*/ $constructor("ZodNullable", (inst, def) => {
	$ZodNullable.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => nullableProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function nullable(innerType) {
	return new ZodNullable({
		type: "nullable",
		innerType
	});
}
const ZodDefault = /*@__PURE__*/ $constructor("ZodDefault", (inst, def) => {
	$ZodDefault.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => defaultProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
	inst.removeDefault = inst.unwrap;
});
function _default(innerType, defaultValue) {
	return new ZodDefault({
		type: "default",
		innerType,
		get defaultValue() {
			return typeof defaultValue === "function" ? defaultValue() : shallowClone(defaultValue);
		}
	});
}
const ZodPrefault = /*@__PURE__*/ $constructor("ZodPrefault", (inst, def) => {
	$ZodPrefault.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => prefaultProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function prefault(innerType, defaultValue) {
	return new ZodPrefault({
		type: "prefault",
		innerType,
		get defaultValue() {
			return typeof defaultValue === "function" ? defaultValue() : shallowClone(defaultValue);
		}
	});
}
const ZodNonOptional = /*@__PURE__*/ $constructor("ZodNonOptional", (inst, def) => {
	$ZodNonOptional.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => nonoptionalProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function nonoptional(innerType, params) {
	return new ZodNonOptional({
		type: "nonoptional",
		innerType,
		...normalizeParams(params)
	});
}
const ZodCatch = /*@__PURE__*/ $constructor("ZodCatch", (inst, def) => {
	$ZodCatch.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => catchProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
	inst.removeCatch = inst.unwrap;
});
function _catch(innerType, catchValue) {
	return new ZodCatch({
		type: "catch",
		innerType,
		catchValue: typeof catchValue === "function" ? catchValue : () => catchValue
	});
}
const ZodPipe = /*@__PURE__*/ $constructor("ZodPipe", (inst, def) => {
	$ZodPipe.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => pipeProcessor(inst, ctx, json, params);
	inst.in = def.in;
	inst.out = def.out;
});
function pipe(in_, out) {
	return new ZodPipe({
		type: "pipe",
		in: in_,
		out
	});
}
const ZodReadonly = /*@__PURE__*/ $constructor("ZodReadonly", (inst, def) => {
	$ZodReadonly.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => readonlyProcessor(inst, ctx, json, params);
	inst.unwrap = () => inst._zod.def.innerType;
});
function readonly(innerType) {
	return new ZodReadonly({
		type: "readonly",
		innerType
	});
}
const ZodCustom = /*@__PURE__*/ $constructor("ZodCustom", (inst, def) => {
	$ZodCustom.init(inst, def);
	ZodType.init(inst, def);
	inst._zod.processJSONSchema = (ctx, json, params) => customProcessor(inst, ctx, json, params);
});
function refine(fn, _params = {}) {
	return /* @__PURE__ */ _refine(ZodCustom, fn, _params);
}
function superRefine(fn, params) {
	return /* @__PURE__ */ _superRefine(fn, params);
}
//#endregion
//#region src/remote-client.ts
/**
* 手写的 Typert Remote 客户端贡献（与官方 typert.remote-client.js 生成工件同构）。
* 由客户端半边经 ctx.remote.$mount() 挂载，得到 ctx.remote.jxSpeech。
*/
const asrStatusSchema = object({
	configured: boolean(),
	model: string()
});
const transcribeResultSchema = object({ text: string() });
const voiceActiveResultSchema = object({ ok: literal(true) });
const localProgressSchema = object({
	file: string(),
	done: number(),
	total: number()
});
const localStatusSchema = object({
	moduleInstalled: boolean(),
	modelReady: boolean(),
	busy: boolean(),
	error: string(),
	progress: localProgressSchema,
	ready: boolean()
});
const localSetupResultSchema = object({ started: literal(true) });
const pcmTranscribeResultSchema = object({ text: string() });
const strParam = (name, symbol) => ({
	name,
	wire: name,
	source: "json",
	codec: {
		mode: "strict",
		typeSymbol: symbol,
		create: () => string()
	}
});
const TYPERT_REMOTE = {
	package: "dsh-speech",
	descriptors: [
		{
			id: "dsh-speech#jxSpeech/asrStatus",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "asrStatus",
			invocation: { kind: "direct" },
			parameters: [],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/asrStatus:result",
				create: () => asrStatusSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/transcribe",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "transcribe",
			invocation: { kind: "direct" },
			parameters: [strParam("audioBase64", "dsh-speech#jxSpeech/transcribe:audioBase64"), strParam("mimeType", "dsh-speech#jxSpeech/transcribe:mimeType")],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/transcribe:result",
				create: () => transcribeResultSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/setVoiceActive",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "setVoiceActive",
			invocation: { kind: "direct" },
			parameters: [{
				name: "active",
				wire: "active",
				source: "json",
				codec: {
					mode: "strict",
					typeSymbol: "dsh-speech#jxSpeech/setVoiceActive:active",
					create: () => boolean()
				}
			}],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/setVoiceActive:result",
				create: () => voiceActiveResultSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/polish",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "polish",
			invocation: { kind: "direct" },
			parameters: [strParam("text", "dsh-speech#jxSpeech/polish:text"), strParam("context", "dsh-speech#jxSpeech/polish:context")],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/polish:result",
				create: () => transcribeResultSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/localStatus",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "localStatus",
			invocation: { kind: "direct" },
			parameters: [],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/localStatus:result",
				create: () => localStatusSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/localSetup",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "localSetup",
			invocation: { kind: "direct" },
			parameters: [],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/localSetup:result",
				create: () => localSetupResultSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		},
		{
			id: "dsh-speech#jxSpeech/transcribePcm",
			service: "jxSpeech",
			namespace: "jxSpeech",
			method: "transcribePcm",
			invocation: { kind: "direct" },
			parameters: [strParam("pcmBase64", "dsh-speech#jxSpeech/transcribePcm:pcmBase64")],
			result: {
				mode: "strict",
				typeSymbol: "dsh-speech#jxSpeech/transcribePcm:result",
				create: () => pcmTranscribeResultSchema
			},
			sourceLocation: {
				file: "src/index.ts",
				line: 1,
				column: 3
			}
		}
	]
};
//#endregion
//#region src/speech-controller.ts
/** 读取浏览器引擎；平台缺失时返回 undefined。 */
function speechEngine() {
	const synthesis = globalThis.speechSynthesis;
	const utteranceCtor = globalThis.SpeechSynthesisUtterance;
	if (synthesis === void 0 || utteranceCtor === void 0) return void 0;
	return synthesis;
}
/** 语言标签是否优先简体中文朗读。 */
function isChineseVoice(lang) {
	return /^zh([-_]cn)?/i.test(lang);
}
/**
* 为一次播放挑选语音。
* @returns 匹配的选择、第一个中文语音、第一个 zh 语音，或 undefined（引擎默认）。
*/
function pickVoice(voices, voiceURI) {
	if (voiceURI !== "") {
		const selected = voices.find((voice) => voice.voiceURI === voiceURI);
		if (selected !== void 0) return selected;
	}
	return voices.find((voice) => isChineseVoice(voice.lang)) ?? voices.find((voice) => /^zh/i.test(voice.lang));
}
/**
* 去除会被念出来的 markdown 语法噪声。
* @param markdown - 渲染态的助手文本。
* @returns 交给语音引擎的文本。
*/
function cleanForSpeech(markdown) {
	return markdown.replace(/(```|~~~)[\s\S]*?(```|~~~)/g, " ").replace(/`([^`]*)`/g, "$1").replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/^#{1,6}\s+/gm, "").replace(/^\s*[-*+]\s+/gm, "").replace(/^\s*\d+\.\s+/gm, "").replace(/^\s*>\s?/gm, "").replace(/\|/g, " ").replace(/[*_~]{1,3}/g, "").replace(/\n{2,}/g, "\n").trim();
}
/**
* 单飞播放会话：同时最多一条发音在播，开始下一条即停掉上一条；
* dispose 取消正在播放的发音。
*/
var SpeechController = class {
	engine;
	stopActive = null;
	disposed = false;
	constructor(engine) {
		this.engine = engine;
	}
	/** 播放一条请求；先停止任何正在进行的播放。 */
	speak(request) {
		if (this.disposed) return;
		this.stop();
		const utterance = new globalThis.SpeechSynthesisUtterance(request.text);
		const voice = pickVoice(this.engine.getVoices(), request.voiceURI);
		if (voice !== void 0) {
			utterance.voice = voice;
			utterance.lang = voice.lang;
		} else utterance.lang = "zh-CN";
		utterance.rate = request.rate;
		let done = false;
		const finish = () => {
			if (done) return;
			done = true;
			this.stopActive = null;
			request.onDone();
		};
		utterance.onend = finish;
		utterance.onerror = finish;
		this.stopActive = () => {
			this.engine.cancel();
			finish();
		};
		this.engine.speak(utterance);
	}
	/** 停止当前播放而不开始新的。 */
	stop() {
		if (this.stopActive !== null) this.stopActive();
	}
	/** 停止播放并拒绝后续所有请求；所属 fiber 卸载时调用。 */
	dispose() {
		this.stop();
		this.disposed = true;
	}
};
//#endregion
//#region src/client/TtsAction.tsx
/**
* 消息操作条上的朗读/停止按钮。通过 chat 快照找到该消息所在 turn-tail 的
* 收尾文本（推理、工具调用、图片永不进入语音引擎），驱动共享播放控制器。
* 自旧坚祥 ui-speech 迁移：取文本改为新版 useChat 快照（V3 节点结构），
* 图标改为内联 SVG（新版 ui-primitives 无喇叭图标）。
*/
/** 收尾块中的纯文本；只有散文会被朗读。 */
function closingText$1(closing) {
	let prose = "";
	for (const block of closing.blocks ?? []) if (block.kind === "text" && typeof block.text === "string") prose += block.text;
	return prose;
}
/** 从 chat 快照中找到收尾于 messageId 的 turn-tail 散文。 */
function messageProse(snapshot, messageId) {
	const nodes = snapshot?.nodes;
	if (nodes === void 0 || typeof nodes.values !== "function") return "";
	for (const node of nodes.values()) {
		if (node.kind !== "turn-tail") continue;
		const closing = node.data?.closing;
		if (closing === void 0 || closing === null) continue;
		if (closing.finalNode?.messageId !== messageId) continue;
		return closingText$1(closing);
	}
	return "";
}
/** 喇叭图标（内联 SVG）。 */
function SpeakerIcon({ stop }) {
	return stop ? /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("svg", {
		width: "16",
		height: "16",
		viewBox: "0 0 16 16",
		fill: "none",
		"aria-hidden": "true",
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
			d: "M8.5 2.5v11a.6.6 0 0 1-.98.46L4.2 11H2.6A1.1 1.1 0 0 1 1.5 9.9V6.1A1.1 1.1 0 0 1 2.6 5h1.6l3.32-2.96A.6.6 0 0 1 8.5 2.5Z",
			fill: "currentColor"
		}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
			d: "M11 6l4 4m0-4l-4 4",
			stroke: "currentColor",
			strokeWidth: "1.4",
			strokeLinecap: "round"
		})]
	}) : /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("svg", {
		width: "16",
		height: "16",
		viewBox: "0 0 16 16",
		fill: "none",
		"aria-hidden": "true",
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
			d: "M8.5 2.5v11a.6.6 0 0 1-.98.46L4.2 11H2.6A1.1 1.1 0 0 1 1.5 9.9V6.1A1.1 1.1 0 0 1 2.6 5h1.6l3.32-2.96A.6.6 0 0 1 8.5 2.5Z",
			fill: "currentColor"
		}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
			d: "M11 5.5a3.6 3.6 0 0 1 0 5M12.8 3.6a6.1 6.1 0 0 1 0 8.8",
			stroke: "currentColor",
			strokeWidth: "1.3",
			strokeLinecap: "round",
			fill: "none"
		})]
	});
}
/**
* 渲染一条已完成助手消息的朗读按钮；没有可朗读散文时不渲染。
*/
function TtsAction({ messageId, useChat, useSpeaking, speak, stop, t }) {
	const text = useChat((snapshot) => messageProse(snapshot, messageId));
	const speaking = useSpeaking((ids) => ids.includes(messageId));
	(0, react.useEffect)(() => () => {
		stop();
	}, [stop]);
	if (typeof text !== "string" || text.trim() === "") return null;
	const label = speaking === true ? t("action.stop") : t("action.speak");
	return /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
		label,
		side: "bottom",
		children: /* @__PURE__ */ (0, react_jsx_runtime.jsx)("button", {
			type: "button",
			style: {
				display: "inline-flex",
				alignItems: "center",
				justifyContent: "center",
				width: 24,
				height: 24,
				padding: 0,
				border: "none",
				background: "transparent",
				cursor: "pointer",
				color: "inherit",
				opacity: .7,
				borderRadius: 6,
				...speaking === true ? {
					opacity: 1,
					color: "#e5484d"
				} : {}
			},
			"aria-label": label,
			"aria-pressed": speaking === true,
			"data-speaking": speaking === true ? "true" : void 0,
			onClick: () => {
				if (speaking === true) {
					stop();
					return;
				}
				speak(messageId, text);
			},
			children: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(SpeakerIcon, { stop: speaking === true })
		})
	});
}
//#endregion
//#region src/settings-shared.ts
/** 语音插件的共享类型与常量（宿主与浏览器两侧共用，不携带宿主依赖）。 */
/** 设置命名空间（宿主注册、浏览器持久化）。 */
const SPEECH_SETTINGS_NAMESPACE = "dsh-speech";
/** 语速字段名。 */
const SPEECH_RATE_FIELD = "rate";
/** 音色字段名（系统语音 URI，'' 为自动挑选中文语音）。 */
const SPEECH_VOICE_FIELD = "voice";
/** 本地离线转写开关字段名。 */
const SPEECH_LOCAL_STT_FIELD = "localStt";
/** 热键选择字段名。 */
const SPEECH_HOTKEY_FIELD = "hotkey";
/** 热键触发模式字段名。 */
const SPEECH_HOTKEY_MODE_FIELD = "hotkeyMode";
/** 润色提示词字段名（'' = 使用内置默认）。 */
const SPEECH_POLISH_PROMPT_FIELD = "polishPrompt";
/** 润色常用词字段名（换行/顿号分隔）。 */
const SPEECH_POLISH_WORDS_FIELD = "polishWords";
/** 润色时是否附上最近 AI 回复作为参考。 */
const SPEECH_POLISH_CONTEXT_FIELD = "polishContext";
/** 语速下限。 */
const SPEECH_RATE_MIN = .5;
/** 语速滑块步进。 */
const SPEECH_RATE_STEP = .1;
/** 默认语速：1x 偏慢，1.3 为 WebView2 实测调校值。 */
const DEFAULT_SPEECH_RATE = 1.3;
/** 润色按钮的出现窗口（毫秒）：听写结束后一段时间内可点。 */
const POLISH_WINDOW_MS = 300 * 1e3;
/** 内置默认润色提示词（设置页"恢复默认"会填入此文本；空提示词也回退到它）。 */
const DEFAULT_POLISH_PROMPT = [
	"以下是用户语音输入的内容，可能有语音输入时词语识别错误或者标点符号不正确。",
	"空格和单独出现的「I.」都代表用户说话中途的停顿。",
	"你需要进行词语谐音修正并合理安排标点符号，",
	"不要改写句式、不要增删内容、不要润色文风、不要解释，只输出修正后的文字本身。"
].join("");
//#endregion
//#region src/client/MicButton.tsx
/**
* 语音输入按钮：Web Speech API 实时听写，直接写入输入框草稿。
* 识别服务瞬时故障（network 等）不立即结束会话：短窗口内退避自动重连，
* 期间用 MediaRecorder 缓冲录音，重连成功后把断线语音补写进草稿
* （云端 ASR 优先；本地 SenseVoice 仅在用户开关开启时作后备，关闭时
* 绝不加载，避免拖垮低配机器）。重连超过 3 秒仍无任何文字输出则报错
* 并丢弃录音，结束本次听写——不让用户说了半天才发现没转写。
* 浏览器完全无识别引擎时回退为 MediaRecorder 录音 → 宿主 ASR 转写。
*
* 自旧坚祥 dsh-file-upload(jx1.0) 的 MicButton 迁移，关键改动：
* - 与文件上传完全解耦（回退路径改走 jxSpeech.transcribe，不再产生音频附件）；
* - 错误提示经输入框 notice 通道（不再依赖上传组件的错误状态）。
* "听写不覆盖用户编辑"的去重/采纳逻辑原样保留。
*/
/** 样式：官方 .add 圆钮规范 + 录音态红/前景色硬切交替 + 倒计时数字弹出缩放。 */
const MIC_STYLE = `
.dsh-mic-btn{display:grid;place-items:center;flex:none;width:28px;height:28px;border:none;border-radius:999px;background:var(--dsw-specific-selector);color:var(--dsw-alias-label-primary);cursor:pointer;position:relative;overflow:visible;transition:background .15s ease}
.dsh-mic-btn:hover{background:var(--dsw-alias-interactive-bg-hover-solid)}
.dsh-mic-btn.recording{animation:jxsp-mic-blink 1s infinite}
@keyframes jxsp-mic-blink{0%,49.99%{color:#e5484d}50%,100%{color:var(--dsw-alias-label-primary)}}
.dsh-mic-count{animation:jxsp-mic-pop .9s ease-out both}
@keyframes jxsp-mic-pop{0%{transform:scale(1.6)}100%{transform:scale(1)}}
`;
/** 两个字符串的最长公共前缀长度。 */
function commonPrefixLen(a, b) {
	const n = Math.min(a.length, b.length);
	let i = 0;
	while (i < n && a[i] === b[i]) i += 1;
	return i;
}
/**
* 浏览器听写（Web Speech）返回链路的标点清洗：全角逗号→一个空格，
* 句号→两个空格，其余标点（、？！；：等）保持不变。只动全角标点——
* 半角逗号/句点常出现在数字（3.14、1,000）与英文文本里，替换会破坏内容。
* 仅对浏览器听写生效（其标点由微软/谷歌服务按停顿猜测，最不可靠）；
* 本地引擎与云端转写回退保留原始标点。
*/
function cleanDictationPunctuation(text) {
	return text.replace(/，/g, " ").replace(/。/g, "  ");
}
/** ArrayBuffer → base64（分块 btoa，避免大文件栈溢出）。 */
function arrayBufferToBase64(buffer) {
	const bytes = new Uint8Array(buffer);
	let binary = "";
	const chunk = 32768;
	for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
	return btoa(binary);
}
/** Float32Array → 小端 base64 字节（本地引擎上传格式）。 */
function f32ToBase64(samples) {
	const bytes = new Uint8Array(samples.length * 4);
	const view = new DataView(bytes.buffer);
	for (let i = 0; i < samples.length; i++) view.setFloat32(i * 4, samples[i], true);
	return arrayBufferToBase64(bytes.buffer);
}
/** 录音 blob → 16kHz 单声道 float32 PCM（浏览器侧解码+重采样，30 秒硬超时防挂起）。 */
async function blobToPcm16k(blob) {
	const Ctx = window.AudioContext;
	if (Ctx === void 0) throw new Error("此浏览器不支持音频解码");
	const ctx = new Ctx();
	const timer = setTimeout(() => {
		ctx.close().catch(() => void 0);
	}, 3e4);
	try {
		const buffer = await ctx.decodeAudioData(await blob.arrayBuffer());
		const src = buffer.getChannelData(0);
		if (buffer.sampleRate === 16e3) return new Float32Array(src);
		const ratio = buffer.sampleRate / 16e3;
		const out = new Float32Array(Math.round(src.length / ratio));
		for (let i = 0; i < out.length; i++) {
			const pos = i * ratio;
			const i0 = Math.floor(pos);
			const i1 = Math.min(i0 + 1, src.length - 1);
			const frac = pos - i0;
			out[i] = src[i0] + (src[i1] - src[i0]) * frac;
		}
		return out;
	} finally {
		clearTimeout(timer);
		ctx.close().catch(() => void 0);
	}
}
/**
* 本地离线听写：MediaRecorder 录 webm，每 3 秒把「头块 + 未转写增量」解码成
* 16kHz PCM 发宿主 SenseVoice 增量转写，文本实时追加进草稿；停止时把剩余
* 尾段转写完。各段文本独立解码、客户端拼接（与 scribe 同策略）。
*/
/** 模块级活跃标志：组件重挂载后旧闭包不可达，用它挡住二次启动与孤儿进程。 */
let localDictationActive = false;
async function runLocalDictationImpl(args) {
	const { transcribePcm, notifyError, updateDraft, readDraft, notifyVoiceActive, onVoiceStart, onVoiceEnd, onState, registerStop, onTeardown, isEarlyCancelled, maxSec, t } = args;
	if (localDictationActive) return;
	localDictationActive = true;
	let stopped = false;
	let recorder = null;
	let previewTimer = null;
	let capTimer = null;
	const chunks = [];
	let previewIndex = 1;
	let baseDraft = "";
	let text = "";
	let lastWritten = null;
	let busy = false;
	const appendText = (delta) => {
		if (delta === "" || stopped) return;
		const cur = readDraft();
		if (lastWritten !== null && cur !== lastWritten) {
			baseDraft = cur;
			text = "";
		}
		text = text === "" ? delta : `${text} ${delta}`;
		lastWritten = baseDraft + text;
		updateDraft(lastWritten);
	};
	const transcribeDelta = async () => {
		if (busy || stopped || chunks.length <= previewIndex) return;
		busy = true;
		try {
			const pcm = await blobToPcm16k(new Blob([chunks[0], ...chunks.slice(previewIndex)]));
			previewIndex = chunks.length;
			appendText(await transcribePcm(f32ToBase64(pcm)));
		} catch (error) {
			const detail = error instanceof Error ? error.message : String(error);
			notifyError(`${t("mic.transcribeFailed")}: ${detail.slice(0, 160)}`);
		} finally {
			busy = false;
		}
	};
	const teardown = () => {
		stopped = true;
		localDictationActive = false;
		if (previewTimer !== null) clearInterval(previewTimer);
		if (capTimer !== null) clearTimeout(capTimer);
		previewTimer = null;
		capTimer = null;
		recorder = null;
		onState(false);
		notifyVoiceActive(false);
		onTeardown();
	};
	try {
		const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
		if (stopped || isEarlyCancelled()) {
			stream.getTracks().forEach((track) => {
				track.stop();
			});
			teardown();
			return;
		}
		const mime = MediaRecorder.isTypeSupported?.("audio/webm") === true ? "audio/webm" : "";
		recorder = new MediaRecorder(stream, mime !== "" ? { mimeType: mime } : void 0);
		recorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		recorder.onstop = () => {
			stream.getTracks().forEach((track) => {
				track.stop();
			});
			teardown();
			(async () => {
				try {
					if (chunks.length > previewIndex) await transcribeDelta();
				} catch {}
				if (text.length > 0 || chunks.length > 1) onVoiceEnd(baseDraft.length, text.length);
			})();
		};
		registerStop(() => {
			try {
				if (recorder !== null && recorder.state !== "inactive") recorder.stop();
			} catch {}
		});
		baseDraft = readDraft();
		onState(true);
		notifyVoiceActive(true);
		onVoiceStart(baseDraft.length);
		recorder.start(1e3);
		previewTimer = setInterval(() => {
			transcribeDelta();
		}, 3e3);
		capTimer = setTimeout(() => {
			recorder?.stop();
		}, maxSec * 1e3);
	} catch (error) {
		const detail = error instanceof Error ? error.message : String(error);
		notifyError(`${t("mic.transcribeFailed")}: ${detail.slice(0, 160)}`);
		teardown();
	}
}
/** 录音并转写回退：MediaRecorder → base64 → 宿主 ASR → 文本追加进草稿。 */
async function recordAndTranscribe(transcribe, notifyError, updateDraft, readDraft, maxSec, t) {
	let recorder = null;
	try {
		const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
		const mime = MediaRecorder.isTypeSupported?.("audio/webm") === true ? "audio/webm" : "";
		recorder = new MediaRecorder(stream, mime !== "" ? { mimeType: mime } : void 0);
		const chunks = [];
		recorder.ondataavailable = (e) => {
			if (e.data.size > 0) chunks.push(e.data);
		};
		recorder.onstop = () => {
			stream.getTracks().forEach((track) => {
				track.stop();
			});
			const blob = new Blob(chunks, { type: mime || "audio/webm" });
			blob.arrayBuffer().then((buffer) => transcribe(arrayBufferToBase64(buffer), blob.type)).then((text) => {
				updateDraft(readDraft() + text);
			}).catch((error) => {
				const detail = error instanceof Error ? error.message : String(error);
				notifyError(`${t("mic.transcribeFailed")}: ${detail.slice(0, 160)}`);
			});
		};
		recorder.start();
		setTimeout(() => {
			recorder?.stop();
		}, maxSec * 1e3);
	} catch (error) {
		recorder = null;
		const detail = error instanceof Error ? error.message : String(error);
		notifyError(`${t("mic.transcribeFailed")}: ${detail.slice(0, 160)}`);
	}
}
/** 渲染语音输入按钮（含录音脉冲与收尾倒计时）。 */
function MicButton({ readDraft, updateDraft, watchSubmit, submitDraft, notifyVoiceActive, transcribe, transcribePcm, localEngine, onVoiceStart, onVoiceEnd, notifyError, hotkey, hotkeyMode, t }) {
	const [recording, setRecording] = (0, react.useState)(false);
	const recRef = (0, react.useRef)(null);
	const timeoutRef = (0, react.useRef)(null);
	const [countdown, setCountdown] = (0, react.useState)(null);
	const countdownRef = (0, react.useRef)(null);
	const sessionRef = (0, react.useRef)(null);
	const gapStopRef = (0, react.useRef)(null);
	const localRef = (0, react.useRef)(null);
	const stop = (silent = false) => {
		const session = sessionRef.current;
		if (session !== null && !silent) session.userStopped = true;
		recRef.current?.stop();
		recRef.current = null;
		localRef.current?.stop();
		localRef.current = null;
		gapStopRef.current?.();
		gapStopRef.current = null;
		setRecording(false);
		if (timeoutRef.current !== null) {
			clearTimeout(timeoutRef.current);
			timeoutRef.current = null;
		}
		if (countdownRef.current !== null) {
			clearInterval(countdownRef.current);
			countdownRef.current = null;
		}
		setCountdown(null);
	};
	const stopRef = (0, react.useRef)(stop);
	stopRef.current = stop;
	const stopRef2 = (0, react.useRef)(() => void 0);
	stopRef2.current = () => {
		stopRef.current();
	};
	const toggleRef = (0, react.useRef)(() => void 0);
	const recordingRef = (0, react.useRef)(false);
	(0, react.useEffect)(() => {
		recordingRef.current = recording;
	}, [recording]);
	const lastHotkeyAt = (0, react.useRef)(0);
	(0, react.useEffect)(() => {
		const matches = (e) => {
			if (hotkey() === "alt-space") return e.code === "Space" && e.altKey && !e.ctrlKey && !e.metaKey;
			return e.key === "Alt" && !e.ctrlKey && !e.shiftKey && !e.metaKey;
		};
		const onKeyDown = (e) => {
			if (e.repeat || !matches(e)) return;
			e.preventDefault();
			const now = Date.now();
			if (now - lastHotkeyAt.current < 250) return;
			lastHotkeyAt.current = now;
			if (hotkeyMode() === "hold") {
				if (!recordingRef.current) toggleRef.current();
			} else toggleRef.current();
		};
		const onKeyUp = (e) => {
			if (hotkeyMode() !== "hold") return;
			if ((hotkey() === "alt-space" ? e.code === "Space" : e.key === "Alt") && recordingRef.current) stopRef2.current();
		};
		const onBlur = () => {
			if (recordingRef.current) stopRef2.current();
		};
		window.addEventListener("keydown", onKeyDown);
		window.addEventListener("keyup", onKeyUp);
		window.addEventListener("blur", onBlur);
		return () => {
			window.removeEventListener("keydown", onKeyDown);
			window.removeEventListener("keyup", onKeyUp);
			window.removeEventListener("blur", onBlur);
		};
	}, [hotkey, hotkeyMode]);
	(0, react.useEffect)(() => watchSubmit(() => {
		const session = sessionRef.current;
		if (session !== null) session.silent = true;
		stopRef.current(true);
	}), [watchSubmit]);
	const toggle = () => {
		if (recording) {
			stop();
			return;
		}
		if (localEngine()) {
			let earlyCancelled = false;
			localRef.current = { stop: () => {
				earlyCancelled = true;
			} };
			runLocalDictationImpl({
				transcribePcm,
				notifyError,
				updateDraft,
				readDraft,
				notifyVoiceActive,
				onVoiceStart,
				onVoiceEnd,
				onState: setRecording,
				registerStop: (handle) => {
					localRef.current = { stop: handle };
				},
				onTeardown: () => {
					localRef.current = null;
				},
				isEarlyCancelled: () => earlyCancelled,
				maxSec: 240,
				t
			});
			startCountdown(240);
			return;
		}
		const SR = window.SpeechRecognition ?? window.webkitSpeechRecognition;
		if (SR === void 0 || SR === null) {
			recordAndTranscribe(transcribe, notifyError, updateDraft, readDraft, 60, t);
			return;
		}
		const rec = new SR();
		rec.lang = navigator.language || "zh-CN";
		rec.continuous = true;
		rec.interimResults = true;
		const session = {
			finalText: "",
			segments: /* @__PURE__ */ new Map(),
			finalized: /* @__PURE__ */ new Set(),
			maxIndex: -1,
			baseDraft: readDraft(),
			lastRendered: readDraft(),
			dedupLeft: "",
			dedupPos: 0,
			dedupConsumed: /* @__PURE__ */ new Map(),
			userStopped: false,
			silent: false,
			pendingSubmit: false,
			netRetries: 0
		};
		sessionRef.current = session;
		const flushSegments = () => {
			const keys = Array.from(session.segments.keys()).sort((a, b) => a - b);
			for (const k of keys) session.finalText += session.segments.get(k) ?? "";
			session.segments.clear();
			session.finalized.clear();
			session.maxIndex = -1;
		};
		const renderLive = () => {
			const cur = readDraft();
			if (cur !== session.lastRendered) {
				session.dedupLeft = session.lastRendered.slice(session.baseDraft.length);
				session.dedupPos = 0;
				session.dedupConsumed.clear();
				session.baseDraft = cur;
				session.lastRendered = cur;
				session.finalText = "";
				session.segments.clear();
				session.finalized.clear();
			}
			let text = session.finalText;
			const keys = Array.from(session.segments.keys()).sort((a, b) => a - b);
			for (const k of keys) text += session.segments.get(k) ?? "";
			const next = session.baseDraft + text;
			if (next === cur) return;
			session.lastRendered = next;
			updateDraft(next);
		};
		let gapStream = null;
		let gapRecorder = null;
		const gapChunks = [];
		const startGapBuffer = () => {
			if (gapRecorder !== null) return;
			navigator.mediaDevices.getUserMedia({ audio: true }).then((stream) => {
				if (gapRecorder !== null || sessionRef.current !== session) {
					stream.getTracks().forEach((track) => {
						track.stop();
					});
					return;
				}
				gapStream = stream;
				const mime = MediaRecorder.isTypeSupported?.("audio/webm") === true ? "audio/webm" : "";
				const recorder = new MediaRecorder(stream, mime !== "" ? { mimeType: mime } : void 0);
				recorder.ondataavailable = (e) => {
					if (e.data.size > 0) gapChunks.push(e.data);
				};
				recorder.start(1e3);
				gapRecorder = recorder;
			}).catch(() => void 0);
		};
		/** 断线缓冲转写：云端优先；本地仅在开关开启时后备；失败返回 ''。 */
		const transcribeGap = async (blob) => {
			try {
				return await transcribe(arrayBufferToBase64(await blob.arrayBuffer()), blob.type);
			} catch {
				if (!localEngine()) return "";
				try {
					return await transcribePcm(f32ToBase64(await blobToPcm16k(blob)));
				} catch {
					return "";
				}
			}
		};
		/** 停止缓冲；discard = true 直接丢弃录音（放弃场景），否则延迟补写。 */
		const stopGapBuffer = (discard = false) => {
			const recorder = gapRecorder;
			gapRecorder = null;
			if (recorder === null) return;
			recorder.onstop = () => {
				gapStream?.getTracks().forEach((track) => {
					track.stop();
				});
				gapStream = null;
				const blob = new Blob(gapChunks, { type: recorder.mimeType || "audio/webm" });
				gapChunks.length = 0;
				if (discard || blob.size === 0) return;
				transcribeGap(blob).then((text) => {
					if (text === "") return;
					if (sessionRef.current === session) {
						session.finalText += text;
						renderLive();
					} else updateDraft(readDraft() + text);
				}).catch(() => void 0);
			};
			try {
				if (recorder.state !== "inactive") recorder.stop();
				else recorder.onstop?.(new Event("stop"));
			} catch {}
		};
		gapStopRef.current = stopGapBuffer;
		/** 断线容忍窗口：持续重连但超过该时长仍无文字输出即放弃（毫秒）。 */
		const GIVE_UP_MS = 3e3;
		let outageStart = null;
		/** 放弃：丢弃缓冲录音、拆除会话、报错告知（避免用户白说）。 */
		const giveUp = () => {
			stopGapBuffer(true);
			recRef.current = null;
			setRecording(false);
			sessionRef.current = null;
			if (timeoutRef.current !== null) {
				clearTimeout(timeoutRef.current);
				timeoutRef.current = null;
			}
			notifyError(t("mic.giveup"));
		};
		/** 退避重启识别（300ms 起、1.2s 封顶），窗口内持续重连。 */
		const restartWithBackoff = () => {
			if (outageStart === null) outageStart = Date.now();
			if (Date.now() - outageStart >= GIVE_UP_MS) {
				giveUp();
				return;
			}
			const delay = Math.min(300 * session.netRetries + 300, 1200);
			session.netRetries += 1;
			startGapBuffer();
			setTimeout(() => {
				if (sessionRef.current !== session) return;
				try {
					rec.start();
				} catch {
					restartWithBackoff();
				}
			}, delay);
		};
		rec.onresult = (event) => {
			if (event.resultIndex < session.maxIndex) flushSegments();
			for (let i = event.resultIndex; i < event.results.length; i += 1) {
				const item = event.results[i];
				if (item.length === 0) continue;
				let transcript = cleanDictationPunctuation(item[0].transcript);
				if (transcript === "") continue;
				if (session.dedupLeft.length > 0) {
					const seen = session.dedupConsumed.get(i) ?? 0;
					if (seen >= transcript.length) continue;
					let growth = transcript.slice(seen);
					const overlap = commonPrefixLen(growth, session.dedupLeft.slice(session.dedupPos));
					session.dedupPos += overlap;
					session.dedupConsumed.set(i, seen + overlap);
					growth = growth.slice(overlap);
					if (growth === "") continue;
					transcript = growth;
				}
				if (item[0].isFinal === true) {
					session.segments.set(i, transcript);
					session.finalized.add(i);
				} else if (!session.finalized.has(i)) session.segments.set(i, transcript);
				session.maxIndex = Math.max(session.maxIndex, i);
			}
			renderLive();
		};
		rec.onstart = () => {
			session.netRetries = 0;
			outageStart = null;
			stopGapBuffer();
		};
		rec.onend = () => {
			const s = sessionRef.current;
			if (s === null) return;
			if (timeoutRef.current !== null) {
				clearTimeout(timeoutRef.current);
				timeoutRef.current = null;
			}
			if (s.userStopped) {
				stopGapBuffer();
				recRef.current = null;
				setRecording(false);
				sessionRef.current = null;
				onVoiceEnd(s.baseDraft.length, s.finalText.length + s.segments.size);
			} else if (s.silent) {
				stopGapBuffer();
				recRef.current = null;
				setRecording(false);
				sessionRef.current = null;
				onVoiceEnd(s.baseDraft.length, s.finalText.length + s.segments.size);
				if (s.pendingSubmit) submitDraft();
			} else {
				const keys = Array.from(s.segments.keys()).sort((a, b) => a - b);
				for (const k of keys) s.finalText += s.segments.get(k) ?? "";
				s.segments.clear();
				s.finalized.clear();
				s.maxIndex = -1;
				restartWithBackoff();
			}
		};
		rec.onerror = (error) => {
			const code = error?.error ?? "";
			const s = sessionRef.current;
			if (code === "not-allowed" || code === "service-not-allowed") {
				stopGapBuffer();
				recRef.current = null;
				setRecording(false);
				sessionRef.current = null;
				if (timeoutRef.current !== null) {
					clearTimeout(timeoutRef.current);
					timeoutRef.current = null;
				}
				notifyError(t("mic.denied"));
				return;
			}
			if (s === null) return;
			startGapBuffer();
		};
		recRef.current = { stop: () => {
			rec.stop();
		} };
		setRecording(true);
		notifyVoiceActive(true);
		onVoiceStart(session.baseDraft.length);
		try {
			rec.start();
		} catch {
			restartWithBackoff();
		}
		timeoutRef.current = setTimeout(() => {
			stopRef.current();
		}, 60 * 1e3);
		startCountdown(60);
	};
	toggleRef.current = toggle;
	/** 录音时长倒计时（最后 9 秒在按钮上显示数字）。 */
	const startCountdown = (maxSec) => {
		const startedAt = Date.now();
		countdownRef.current = setInterval(() => {
			if (sessionRef.current === null && localRef.current === null) {
				if (countdownRef.current !== null) {
					clearInterval(countdownRef.current);
					countdownRef.current = null;
				}
				setCountdown(null);
				return;
			}
			const remaining = maxSec * 1e3 - (Date.now() - startedAt);
			if (remaining <= 0) {
				if (countdownRef.current !== null) {
					clearInterval(countdownRef.current);
					countdownRef.current = null;
				}
				setCountdown(null);
				stopRef.current();
				return;
			}
			const sec = Math.ceil(remaining / 1e3);
			setCountdown(sec <= 9 ? sec : null);
		}, 200);
	};
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("style", { children: MIC_STYLE }), /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
		label: recording ? t("mic.stop") : t("mic.start"),
		side: "top",
		children: /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("button", {
			type: "button",
			className: `dsh-mic-btn${recording ? " recording" : ""}`,
			"aria-label": recording ? t("mic.stop") : t("mic.start"),
			"aria-pressed": recording,
			onClick: toggle,
			onKeyDown: (e) => {
				if (e.key === "Enter" && sessionRef.current !== null) {
					e.preventDefault();
					const session = sessionRef.current;
					session.pendingSubmit = true;
					session.silent = true;
					stopRef.current(true);
				}
			},
			children: [/* @__PURE__ */ (0, react_jsx_runtime.jsxs)("svg", {
				width: "16",
				height: "16",
				viewBox: "0 0 16 16",
				fill: "none",
				"aria-hidden": "true",
				children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
					d: "M8 1.5a2.5 2.5 0 0 0-2.5 2.5v4a2.5 2.5 0 0 0 5 0V4A2.5 2.5 0 0 0 8 1.5Z",
					fill: "currentColor"
				}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("path", {
					d: "M3.5 7.5a.75.75 0 0 1 1.5 0 2.5 2.5 0 0 0 5 0 .75.75 0 0 1 1.5 0 4 4 0 0 1-3.25 3.94V13H10a.75.75 0 0 1 0 1.5H6A.75.75 0 0 1 6 13h1.75v-1.56A4 4 0 0 1 4.5 8a.75.75 0 0 1 .5-.75.75.75 0 0 1 .5-.5Z",
					fill: "currentColor",
					transform: "translate(0 -1)"
				})]
			}), countdown !== null && /* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
				className: "dsh-mic-count",
				style: {
					position: "absolute",
					top: -5,
					right: -8,
					minWidth: 16,
					height: 16,
					lineHeight: "16px",
					textAlign: "center",
					fontSize: 11,
					fontWeight: 700,
					borderRadius: 8,
					background: "#e5484d",
					color: "#fff",
					padding: "0 3px",
					boxSizing: "border-box"
				},
				children: countdown
			}, countdown)]
		})
	})] });
}
//#endregion
//#region src/client/PolishBubble.tsx
/** 收尾块中的纯文本。 */
function closingText(closing) {
	let prose = "";
	for (const block of closing.blocks ?? []) if (block.kind === "text" && typeof block.text === "string") prose += block.text;
	return prose;
}
/** 从 chat 快照取最后一条助手消息的纯文本（无则空串）。 */
function lastAssistantText(snapshot) {
	const nodes = snapshot?.nodes;
	if (nodes === void 0 || typeof nodes.values !== "function") return "";
	let last = "";
	for (const node of nodes.values()) {
		if (node.kind !== "turn-tail") continue;
		const closing = node.data?.closing;
		if (closing === void 0 || closing === null) continue;
		const text = closingText(closing);
		if (text.trim() !== "") last = text;
	}
	return last;
}
const WRAP = {
	position: "absolute",
	right: "22%",
	top: -40,
	zIndex: 30,
	display: "flex",
	alignItems: "flex-end",
	flexDirection: "column"
};
const BUBBLE = {
	position: "relative",
	display: "inline-flex",
	alignItems: "center",
	gap: 6,
	padding: "5px 12px",
	borderRadius: "12px 12px 12px 4px",
	border: "1px solid var(--dsw-alias-border-l2)",
	background: "var(--dsw-alias-bg-module-platform)",
	color: "var(--dsw-alias-label-primary)",
	boxShadow: "0 4px 14px rgba(0,0,0,0.12)",
	font: "inherit",
	fontSize: 13,
	lineHeight: "20px",
	cursor: "pointer",
	whiteSpace: "nowrap"
};
const TAIL = {
	position: "absolute",
	left: -5,
	bottom: 8,
	width: 10,
	height: 10,
	background: "var(--dsw-alias-bg-module-platform)",
	borderLeft: "1px solid var(--dsw-alias-border-l2)",
	borderBottom: "1px solid var(--dsw-alias-border-l2)",
	transform: "rotate(45deg)",
	borderRadius: "0 0 0 3px"
};
const DOT = {
	width: 6,
	height: 6,
	borderRadius: 999,
	flex: "none",
	background: "var(--dsw-alias-label-primary)",
	animation: "jxsp-polish-blink 1.2s ease-in-out infinite"
};
/** 渲染漫画气泡润色按钮。 */
function PolishBubble({ usePolish, polish, dismiss, useContext, useChat, t }) {
	const state = usePolish((value) => value);
	const snapshot = useChat((s) => s);
	if (state.visible !== true) return null;
	const runPolish = () => {
		polish(useContext() ? lastAssistantText(snapshot) : "").catch(() => void 0);
	};
	const label = state.error !== "" ? state.error : state.busy === true ? t("polish.busy") : t("polish.button");
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: WRAP,
		children: [
			/* @__PURE__ */ (0, react_jsx_runtime.jsxs)("button", {
				type: "button",
				style: {
					...BUBBLE,
					...state.error !== "" ? {
						borderColor: "#e5484d",
						color: "#e5484d"
					} : {}
				},
				disabled: state.busy,
				title: state.error !== "" ? state.error : void 0,
				onClick: runPolish,
				children: [
					/* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
						style: TAIL,
						"aria-hidden": "true"
					}),
					state.busy === true && /* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", { style: DOT }),
					/* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
						style: {
							whiteSpace: "nowrap",
							overflow: "hidden",
							textOverflow: "ellipsis"
						},
						children: label
					})
				]
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)("style", { children: "@keyframes jxsp-polish-blink{0%,100%{opacity:.25}50%{opacity:1}}" }),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)("button", {
				type: "button",
				"aria-label": "dismiss",
				style: {
					position: "absolute",
					inset: -12,
					zIndex: -1,
					background: "transparent",
					border: "none",
					cursor: "default"
				},
				onClick: dismiss,
				tabIndex: -1
			})
		]
	});
}
//#endregion
//#region src/client/SpeechSettingsSection.tsx
/**
* 语音设置页：语速、音色、试听三行。
* 行结构/字体/间距/控件复用官方 General 设置行的规范（逐值参照
* ui-chat TranscriptViewRow 的行结构与 CSS 变量：--dsw-alias-label-primary /
* --dsw-alias-label-tertiary / --dsw-alias-border-l2 / --dsw-alias-bg-module-platform /
* --dsw-alias-interactive-bg-hover），下拉选择复用官方 ui-primitives 的
* Menu 组件（与"对话显示"行同款交互）。
* 偏好经 settingsScope 持久化到宿主用户设置文档（命名空间 dsh-speech）。
*/
/** 行结构样式：逐值复刻官方 TranscriptViewRow.module.css 的行规范。 */
const ROW = {
	display: "flex",
	alignItems: "center",
	gap: 8,
	padding: "16px 0",
	borderBottom: "0.5px solid var(--dsw-alias-border-l2)"
};
const ROW_TEXT = {
	flex: 1,
	minWidth: 0,
	display: "flex",
	flexDirection: "column",
	gap: 4,
	paddingRight: 48
};
const TITLE = {
	fontSize: 14,
	fontWeight: 400,
	lineHeight: "22px",
	color: "var(--dsw-alias-label-primary)"
};
const DESC = {
	fontSize: 12,
	fontWeight: 400,
	lineHeight: "18px",
	color: "var(--dsw-alias-label-tertiary)"
};
/** 药丸选择钮：与官方 .selector 同规格（36px 高、18px 圆角、模块底色）。 */
const PILL = {
	display: "inline-flex",
	alignItems: "center",
	gap: 12,
	height: 36,
	padding: "0 14px",
	flex: "none",
	border: "none",
	borderRadius: 18,
	background: "var(--dsw-alias-bg-module-platform)",
	font: "inherit",
	fontSize: 14,
	lineHeight: "22px",
	color: "var(--dsw-alias-label-primary)",
	cursor: "pointer",
	maxWidth: 280,
	whiteSpace: "nowrap",
	overflow: "hidden"
};
const PILL_LABEL = {
	overflow: "hidden",
	textOverflow: "ellipsis"
};
const CHEVRON = { flex: "none" };
const SECTION = {
	display: "flex",
	flexDirection: "column",
	padding: "0 4px"
};
/** 语速滑块组：滑块 + 当前倍率值。 */
const RATE_CONTROL = {
	display: "flex",
	alignItems: "center",
	gap: 12,
	flex: "none"
};
const SLIDER = {
	width: 220,
	accentColor: "#6b7a90",
	cursor: "pointer"
};
const RATE_VALUE = {
	width: 44,
	textAlign: "right",
	fontVariantNumeric: "tabular-nums",
	fontSize: 14,
	lineHeight: "22px",
	color: "var(--dsw-alias-label-primary)"
};
/** 行组件：官方规范的标签 + 描述 + 控件。 */
function Row({ title, desc, control }) {
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: ROW,
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
			style: ROW_TEXT,
			children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("div", {
				style: TITLE,
				children: title
			}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("div", {
				style: DESC,
				children: desc
			})]
		}), control]
	});
}
/** 语速行控件：保留旧版连续滑块（0.5–3.0，步进 0.1）。 */
function RateControl({ rate, setRate }) {
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: RATE_CONTROL,
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("input", {
			type: "range",
			style: SLIDER,
			min: SPEECH_RATE_MIN,
			max: 3,
			step: SPEECH_RATE_STEP,
			value: rate,
			"aria-label": "rate",
			onChange: (event) => {
				setRate(Number(event.target.value));
			}
		}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
			style: RATE_VALUE,
			children: `${rate.toFixed(1)}x`
		})]
	});
}
/** 本地引擎行控件：苹果样式开关 + 状态/进度文本。 */
function LocalControl({ value, status, set, refresh, t }) {
	const [on, setOn] = (0, react.useState)(value);
	if (on !== value) setOn(value);
	let detail = "";
	if (status !== void 0) {
		if (status.error !== "") detail = `${t("local.error")}: ${status.error.slice(0, 40)}`;
		else if (status.busy && status.progress.total > 0) {
			const pct = Math.floor(status.progress.done / status.progress.total * 100);
			detail = `${t("local.downloading")} ${pct}%`;
		} else if (status.busy) detail = t("local.installing");
		else if (status.ready && on) detail = t("local.ready");
	}
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: {
			display: "flex",
			alignItems: "center",
			gap: 10,
			flex: "none"
		},
		children: [detail !== "" ? /* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
			style: {
				fontSize: 12,
				color: "var(--dsw-alias-label-tertiary)"
			},
			children: detail
		}) : null, /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Switch, {
			className: "jx-blue-switch",
			checked: on,
			label: t("local.title"),
			onChange: (next) => {
				setOn(next);
				set(next);
				if (next) refresh();
			}
		})]
	});
}
/** 宽行：标签 + 描述在上，控件占满整行（文本编辑类）。 */
function WideRow({ title, desc, children }) {
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: {
			...ROW,
			flexDirection: "column",
			alignItems: "stretch",
			gap: 8
		},
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
			style: {
				...ROW_TEXT,
				paddingRight: 0
			},
			children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("div", {
				style: TITLE,
				children: title
			}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("div", {
				style: DESC,
				children: desc
			})]
		}), children]
	});
}
const TEXTAREA = {
	width: "100%",
	boxSizing: "border-box",
	minHeight: 84,
	padding: "8px 10px",
	borderRadius: 10,
	border: "1px solid var(--dsw-alias-border-l2)",
	background: "var(--dsw-alias-bg-module-platform)",
	color: "var(--dsw-alias-label-primary)",
	font: "inherit",
	fontSize: 13,
	lineHeight: "20px",
	resize: "vertical"
};
const TEXTAREA_TOOLBAR = {
	display: "flex",
	justifyContent: "flex-end",
	marginTop: 6
};
/** 渲染语音设置行。 */
function SpeechSettingsSection({ setRate, setVoice, preview, stopPreview, usePreviewing, useSettings, engine, setHotkey, setHotkeyMode, setPolishPrompt, setPolishWords, setPolishContext, useLocal, value, set, refresh, t }) {
	const settings = useSettings((v) => v);
	const previewing = usePreviewing((v) => v);
	const localStatus = useLocal((v) => v);
	const { rate, voice } = settings;
	const [hotkeyOpen, setHotkeyOpen] = (0, react.useState)(false);
	const [modeOpen, setModeOpen] = (0, react.useState)(false);
	const [voiceOpen, setVoiceOpen] = (0, react.useState)(false);
	const [promptDraft, setPromptDraft] = (0, react.useState)(null);
	const [wordsDraft, setWordsDraft] = (0, react.useState)(null);
	const promptValue = promptDraft ?? settings.polishPrompt;
	const wordsValue = wordsDraft ?? settings.polishWords;
	const [voices, setVoices] = (0, react.useState)(() => engine.getVoices());
	(0, react.useEffect)(() => {
		const update = () => {
			setVoices(engine.getVoices());
		};
		update();
		engine.addEventListener("voiceschanged", update);
		return () => {
			engine.removeEventListener("voiceschanged", update);
		};
	}, [engine]);
	const sorted = [...voices].sort((a, b) => {
		const az = /^zh/i.test(a.lang) ? 0 : 1;
		const bz = /^zh/i.test(b.lang) ? 0 : 1;
		if (az !== bz) return az - bz;
		return a.name.localeCompare(b.name);
	});
	const voiceLabel = voice === "" ? t("settings.voiceAuto") : sorted.find((candidate) => candidate.voiceURI === voice)?.name ?? voice;
	const voiceSelector = /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("button", {
		type: "button",
		style: PILL,
		"aria-haspopup": "menu",
		"aria-expanded": voiceOpen,
		title: voiceLabel,
		onClick: () => {
			setVoiceOpen((value) => !value);
		},
		children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("span", {
			style: PILL_LABEL,
			children: voiceLabel
		}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, { style: CHEVRON })]
	});
	return /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("div", {
		style: SECTION,
		children: [
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)("style", { children: ".jx-blue-switch[aria-checked=\"true\"]{background:#4d6bfe}" }),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("settings.rate"),
				desc: t("settings.rateHint"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(RateControl, {
					rate,
					setRate
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("settings.voice"),
				desc: t("settings.voiceHint"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Menu, {
					open: voiceOpen,
					onClose: () => {
						setVoiceOpen(false);
					},
					items: [{
						id: "",
						label: t("settings.voiceAuto")
					}, ...sorted.map((candidate) => ({
						id: candidate.voiceURI,
						label: `${candidate.name} (${candidate.lang})`
					}))],
					selectedId: voice,
					onSelect: (id) => {
						setVoiceOpen(false);
						setVoice(id);
					},
					align: "end",
					portal: true,
					anchor: voiceSelector
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("settings.preview"),
				desc: t("settings.previewHint"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)("button", {
					type: "button",
					style: PILL,
					onClick: previewing === true ? stopPreview : preview,
					children: previewing === true ? t("settings.previewStop") : t("settings.preview")
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("local.title"),
				desc: t("local.desc"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(LocalControl, {
					value: value(),
					status: localStatus,
					set,
					refresh,
					t
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("hotkey.title"),
				desc: t("hotkey.desc"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Menu, {
					open: hotkeyOpen,
					onClose: () => {
						setHotkeyOpen(false);
					},
					items: [{
						id: "alt",
						label: t("hotkey.alt")
					}, {
						id: "alt-space",
						label: t("hotkey.altSpace")
					}],
					selectedId: settings.hotkey,
					onSelect: (id) => {
						setHotkeyOpen(false);
						setHotkey(id);
					},
					align: "end",
					portal: true,
					anchor: /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("button", {
						type: "button",
						style: PILL,
						"aria-haspopup": "menu",
						"aria-expanded": hotkeyOpen,
						onClick: () => {
							setHotkeyOpen((value) => !value);
						},
						children: [settings.hotkey === "alt-space" ? t("hotkey.altSpace") : t("hotkey.alt"), /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, { style: CHEVRON })]
					})
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("hotkeyMode.title"),
				desc: t("hotkeyMode.desc"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Menu, {
					open: modeOpen,
					onClose: () => {
						setModeOpen(false);
					},
					items: [{
						id: "tap",
						label: t("hotkeyMode.tap")
					}, {
						id: "hold",
						label: t("hotkeyMode.hold")
					}],
					selectedId: settings.hotkeyMode,
					onSelect: (id) => {
						setModeOpen(false);
						setHotkeyMode(id);
					},
					align: "end",
					portal: true,
					anchor: /* @__PURE__ */ (0, react_jsx_runtime.jsxs)("button", {
						type: "button",
						style: PILL,
						"aria-haspopup": "menu",
						"aria-expanded": modeOpen,
						onClick: () => {
							setModeOpen((value) => !value);
						},
						children: [settings.hotkeyMode === "hold" ? t("hotkeyMode.hold") : t("hotkeyMode.tap"), /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, { style: CHEVRON })]
					})
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsxs)(WideRow, {
				title: t("prompt.title"),
				desc: t("prompt.desc"),
				children: [/* @__PURE__ */ (0, react_jsx_runtime.jsx)("textarea", {
					style: TEXTAREA,
					value: promptValue,
					placeholder: t("prompt.placeholder"),
					onChange: (event) => {
						setPromptDraft(event.target.value);
					},
					onBlur: () => {
						setPolishPrompt(promptValue);
						setPromptDraft(null);
					}
				}), /* @__PURE__ */ (0, react_jsx_runtime.jsx)("div", {
					style: TEXTAREA_TOOLBAR,
					children: /* @__PURE__ */ (0, react_jsx_runtime.jsx)("button", {
						type: "button",
						style: PILL,
						onClick: () => {
							setPromptDraft(DEFAULT_POLISH_PROMPT);
							setPolishPrompt(DEFAULT_POLISH_PROMPT);
						},
						children: t("prompt.reset")
					})
				})]
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(WideRow, {
				title: t("words.title"),
				desc: t("words.desc"),
				children: /* @__PURE__ */ (0, react_jsx_runtime.jsx)("textarea", {
					style: {
						...TEXTAREA,
						minHeight: 64
					},
					value: wordsValue,
					placeholder: t("words.placeholder"),
					onChange: (event) => {
						setWordsDraft(event.target.value);
					},
					onBlur: () => {
						setPolishWords(wordsValue);
						setWordsDraft(null);
					}
				})
			}),
			/* @__PURE__ */ (0, react_jsx_runtime.jsx)(Row, {
				title: t("context.title"),
				desc: t("context.desc"),
				control: /* @__PURE__ */ (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Switch, {
					className: "jx-blue-switch",
					checked: settings.polishContext,
					label: t("context.title"),
					onChange: setPolishContext
				})
			})
		]
	});
}
//#endregion
//#region src/locales.ts
/** 语音插件界面文案（朗读按钮、语音输入按钮、语音设置页）。 */
/** 简体中文词典与键的唯一来源。 */
const zh = {
	"nav": "语音",
	"action.speak": "朗读",
	"action.stop": "停止朗读",
	"settings.rate": "朗读语速",
	"settings.rateHint": "数值越大读得越快",
	"settings.voice": "朗读音色",
	"settings.voiceHint": "来自系统语音，默认自动选择中文语音",
	"settings.voiceAuto": "自动（优先中文）",
	"settings.preview": "朗读试听",
	"settings.previewHint": "用当前设置播放一段示例语音",
	"settings.previewStop": "停止",
	"mic.start": "语音输入",
	"mic.stop": "停止录音",
	"mic.denied": "麦克风权限被拒绝，请在浏览器设置中允许使用麦克风",
	"mic.srUnavailable": "当前浏览器不支持实时语音识别",
	"mic.fallback": "语音识别不可用，已改用录音转写",
	"mic.giveup": "语音识别连接失败，本次听写已停止；刚才说的内容未能转写，请稍后重试",
	"mic.transcribing": "转写中…",
	"mic.transcribeFailed": "语音转写失败",
	"mic.noAsr": "当前浏览器不支持实时语音识别，且服务端未配置转写（ASR）",
	"polish.button": "润色：由大模型修正错字和标点符号",
	"polish.busy": "润色中…",
	"polish.failed": "润色失败",
	"local.title": "本地语音转文字（离线）",
	"local.desc": "下载依赖（约 230MB，只需一次）后离线识别，音频不出本机",
	"local.on": "启用",
	"local.off": "未启用",
	"local.installing": "安装组件中…",
	"local.downloading": "下载模型中",
	"local.ready": "已就绪",
	"local.error": "出错",
	"hotkey.title": "语音输入热键",
	"hotkey.desc": "不用鼠标，按键直接开始/结束语音输入",
	"hotkey.alt": "Alt",
	"hotkey.altSpace": "Alt + 空格",
	"hotkeyMode.title": "触发方式",
	"hotkeyMode.desc": "点按：按一下开始，再按结束；按住说话：松开即结束转写",
	"hotkeyMode.tap": "点按",
	"hotkeyMode.hold": "按住说话",
	"prompt.title": "润色提示词",
	"prompt.desc": "控制 ✨润色 按钮如何修正语音文本；留空使用内置默认提示词，常用词自动附在末尾",
	"prompt.placeholder": "（留空 = 使用内置默认提示词）",
	"prompt.reset": "恢复默认",
	"words.title": "润色常用词",
	"words.desc": "人名、术语、项目名等易被谐音听错的词，每行一个（也可用顿号分隔）",
	"words.placeholder": "例如：\n坚祥数字助理\nBOM 清单\nMES 系统",
	"context.title": "润色参考最近 AI 回复",
	"context.desc": "开启后，润色时会把本会话最近一次 AI 回复的纯文本一并交给模型，帮助消歧谐音词（不修改该回复）"
};
/** 英文词典（键集合与中文一致）。 */
const en = {
	"nav": "Speech",
	"action.speak": "Speak",
	"action.stop": "Stop speaking",
	"settings.rate": "Read-aloud rate",
	"settings.rateHint": "Higher values read faster",
	"settings.voice": "Read-aloud voice",
	"settings.voiceHint": "System voices; default auto-picks a Chinese voice",
	"settings.voiceAuto": "Auto (Chinese preferred)",
	"settings.preview": "Read-aloud preview",
	"settings.previewHint": "Play a sample with the current settings",
	"settings.previewStop": "Stop",
	"mic.start": "Voice input",
	"mic.stop": "Stop recording",
	"mic.denied": "Microphone permission denied; allow it in browser settings",
	"mic.srUnavailable": "Live speech recognition is unavailable in this browser",
	"mic.fallback": "Speech recognition unavailable; fell back to recording",
	"mic.giveup": "Speech recognition could not reconnect; dictation stopped. Nothing was transcribed — please try again shortly",
	"mic.transcribing": "Transcribing…",
	"mic.transcribeFailed": "Transcription failed",
	"mic.noAsr": "No live recognition here, and no server-side ASR configured",
	"polish.button": "Polish: AI fixes typos and punctuation",
	"polish.busy": "Polishing…",
	"polish.failed": "Polish failed",
	"local.title": "Local speech-to-text (offline)",
	"local.desc": "Download deps (~230MB, once) for offline recognition; audio never leaves this machine",
	"local.on": "Enabled",
	"local.off": "Off",
	"local.installing": "Installing…",
	"local.downloading": "Downloading",
	"local.ready": "Ready",
	"local.error": "Error",
	"hotkey.title": "Voice input hotkey",
	"hotkey.desc": "Start/stop voice input without the mouse",
	"hotkey.alt": "Alt",
	"hotkey.altSpace": "Alt + Space",
	"hotkeyMode.title": "Trigger mode",
	"hotkeyMode.desc": "Tap: press once to start, again to stop; Hold: release to finish",
	"hotkeyMode.tap": "Tap",
	"hotkeyMode.hold": "Push to talk",
	"prompt.title": "Polish prompt",
	"prompt.desc": "How the ✨Polish button fixes speech text; empty = built-in default, common words appended automatically",
	"prompt.placeholder": "(empty = built-in default prompt)",
	"prompt.reset": "Reset",
	"words.title": "Common words",
	"words.desc": "Names and terms the recognizer tends to get wrong, one per line",
	"words.placeholder": "e.g.\nproject codenames\ntechnical terms",
	"context.title": "Include last AI reply as polish context",
	"context.desc": "Send the latest AI reply text with the polish request to help disambiguate homophones (the reply itself is never modified)"
};
//#endregion
//#region src/client.tsx
/** 词典命名空间。 */
const NS = "dsh-speech";
/** 顶层插件所需服务（$mount 的载体；命名空间服务由本插件自己挂出来）。 */
const inject = ["remote"];
/** 冻结的偏好兜底：uSES 要求 getSnapshot 引用稳定。 */
const FALLBACK_SETTINGS = Object.freeze({
	rate: DEFAULT_SPEECH_RATE,
	voice: "",
	localStt: false,
	hotkey: "alt",
	hotkeyMode: "tap",
	polishPrompt: "",
	polishWords: "",
	polishContext: false
});
const PREVIEW_SENTENCE = "您好，这是当前语速和音色的试听效果。";
/** 挂载 jxSpeech Remote，并装配朗读/语音输入/润色/设置页四块界面。 */
async function apply(ctx) {
	const disposeMount = await ctx.remote.$mount(TYPERT_REMOTE);
	const disposeUI = ctx.plugin({
		name: "dsh-speech/ui",
		inject: [
			"slots",
			"locale",
			"remote.jxSpeech",
			"settingsScope",
			"sessions"
		],
		apply(child) {
			const engine = speechEngine();
			const remote = child.remote;
			const unwrap = (result, what) => {
				if (!result.ok) throw new Error(`${what} failed: ${result.error.code}: ${result.error.message}`);
				return result.value;
			};
			child.effect(() => child.locale.register(NS, {
				zh,
				en
			}), "dsh-speech: dictionaries");
			const scope = child.settingsScope.bind({ namespace: SPEECH_SETTINGS_NAMESPACE });
			const currentSettings = () => scope.getSnapshot().value ?? FALLBACK_SETTINGS;
			let localReady = false;
			let localState;
			const localListeners = /* @__PURE__ */ new Set();
			const localSource = {
				getSnapshot: () => localState,
				subscribe(fn) {
					localListeners.add(fn);
					return () => {
						localListeners.delete(fn);
					};
				}
			};
			const pushLocal = (value) => {
				localState = value;
				localReady = value?.ready === true;
				for (const fn of localListeners) fn();
			};
			const refreshLocal = () => {
				remote.jxSpeech.localStatus().then((result) => {
					if (result.ok) pushLocal(result.value);
				}).catch(() => void 0);
			};
			refreshLocal();
			let speakingIds = [];
			const speakingListeners = /* @__PURE__ */ new Set();
			const speakingSource = {
				getSnapshot: () => speakingIds,
				subscribe(fn) {
					speakingListeners.add(fn);
					return () => {
						speakingListeners.delete(fn);
					};
				}
			};
			const setSpeakingIds = (next) => {
				if (next.length === speakingIds.length && next.every((id, i) => id === speakingIds[i])) return;
				speakingIds = next;
				for (const fn of speakingListeners) fn();
			};
			let previewing = false;
			const previewListeners = /* @__PURE__ */ new Set();
			const previewSource = {
				getSnapshot: () => previewing,
				subscribe(fn) {
					previewListeners.add(fn);
					return () => {
						previewListeners.delete(fn);
					};
				}
			};
			const setPreviewing = (next) => {
				if (next === previewing) return;
				previewing = next;
				for (const fn of previewListeners) fn();
			};
			let polishState = {
				visible: false,
				busy: false,
				error: ""
			};
			/** 语音段在草稿中的起点（听写开始时的草稿长度）。 */
			let voiceStart = null;
			let polishHideTimer = null;
			const polishListeners = /* @__PURE__ */ new Set();
			const polishSource = {
				getSnapshot: () => polishState,
				subscribe(fn) {
					polishListeners.add(fn);
					return () => {
						polishListeners.delete(fn);
					};
				}
			};
			const setPolishState = (next) => {
				polishState = next;
				for (const fn of polishListeners) fn();
			};
			/** 语音输入开启即亮气泡（告知效果），并刷新存在窗口。 */
			const showPolish = (start) => {
				voiceStart = start;
				setPolishState({
					visible: true,
					busy: false,
					error: ""
				});
				if (polishHideTimer !== null) clearTimeout(polishHideTimer);
				polishHideTimer = setTimeout(hidePolish, POLISH_WINDOW_MS);
			};
			const hidePolish = () => {
				if (polishHideTimer !== null) {
					clearTimeout(polishHideTimer);
					polishHideTimer = null;
				}
				voiceStart = null;
				setPolishState({
					visible: false,
					busy: false,
					error: ""
				});
			};
			const inputFaceOf = (sessionId) => {
				const actx = child.sessions?.scope(sessionId);
				if (actx === void 0) return void 0;
				return actx.get("conversation")?.input.for(actx);
			};
			if (engine !== void 0) {
				const controller = new SpeechController(engine);
				child.effect(() => () => {
					controller.dispose();
				}, "dsh-speech: playback disposal");
				engine.getVoices();
				const ttsInjected = () => ({
					hooks: { speaking: speakingSource },
					speak: (messageId, text) => {
						controller.speak({
							rate: currentSettings().rate,
							voiceURI: currentSettings().voice,
							text: cleanForSpeech(text),
							onDone: () => {
								setSpeakingIds(speakingIds.filter((id) => id !== messageId));
							}
						});
						setSpeakingIds([...speakingIds, messageId]);
					},
					stop: () => {
						controller.stop();
					}
				});
				const settingsInjected = () => ({
					hooks: {
						previewing: previewSource,
						settings: {
							getSnapshot: () => currentSettings(),
							subscribe: (fn) => scope.subscribe(fn)
						}
					},
					setRate: (rate) => {
						scope.set(SPEECH_RATE_FIELD, rate);
					},
					setVoice: (voiceURI) => {
						scope.set(SPEECH_VOICE_FIELD, voiceURI);
					},
					setHotkey: (key) => {
						scope.set(SPEECH_HOTKEY_FIELD, key);
					},
					setHotkeyMode: (mode) => {
						scope.set(SPEECH_HOTKEY_MODE_FIELD, mode);
					},
					setPolishPrompt: (text) => {
						scope.set(SPEECH_POLISH_PROMPT_FIELD, text);
					},
					setPolishWords: (text) => {
						scope.set(SPEECH_POLISH_WORDS_FIELD, text);
					},
					setPolishContext: (on) => {
						scope.set(SPEECH_POLISH_CONTEXT_FIELD, on);
					},
					preview: () => {
						const settings = currentSettings();
						controller.speak({
							rate: settings.rate,
							voiceURI: settings.voice,
							text: PREVIEW_SENTENCE,
							onDone: () => {
								setPreviewing(false);
							}
						});
						setPreviewing(true);
					},
					stopPreview: () => {
						controller.stop();
					},
					engine
				});
				const localFace = () => ({
					hooks: { local: localSource },
					value: () => currentSettings().localStt,
					set: (on) => {
						scope.set(SPEECH_LOCAL_STT_FIELD, on);
						if (on) {
							remote.jxSpeech.localSetup().then((result) => {
								if (!result.ok) throw new Error(result.error.message);
							}).catch((error) => {
								console.warn("[dsh-speech] localSetup:", error);
							});
							startLocalPolling();
						}
					},
					refresh: refreshLocal
				});
				child.slots.inject("conversation.chat.assistant-actions", () => child.slots.register({
					name: "conversation.chat.assistant-actions",
					id: "dsh-speech-tts",
					order: 20,
					locale: NS,
					inject: ttsInjected
				}, TtsAction));
				child.slots.inject("settings.section", () => child.slots.register({
					name: "settings.section",
					id: "speech",
					order: 15,
					locale: NS,
					label: () => child.locale.bind(NS)("nav"),
					inject: () => {
						const base = settingsInjected();
						const local = localFace();
						return {
							...base,
							...local,
							hooks: {
								...base.hooks,
								...local.hooks
							}
						};
					}
				}, SpeechSettingsSection));
			}
			/** 下载中每 2 秒轮询本地状态，就绪或出错后停止。 */
			const startLocalPolling = () => {
				const timer = setInterval(() => {
					refreshLocal();
					if (localState !== void 0 && !localState.busy) clearInterval(timer);
				}, 2e3);
				child.effect(() => () => clearInterval(timer), "dsh-speech: local polling");
			};
			child.slots.inject("conversation.input.right", () => child.slots.register({
				name: "conversation.input.right",
				id: "dsh-speech-mic",
				order: 0,
				locale: NS,
				inject: (sessionId) => {
					const inputFace = () => inputFaceOf(sessionId);
					const notifyError = (text) => {
						const input = inputFace();
						if (typeof input?.notify === "function") input.notify("error", text);
						else console.warn(`[dsh-speech] ${text}`);
					};
					return {
						readDraft: () => inputFace()?.state.getSnapshot().draft ?? "",
						updateDraft: (text) => {
							inputFace()?.setDraft(text);
						},
						watchSubmit: (onSubmit) => {
							const input = inputFace();
							if (input === void 0) return () => void 0;
							return input.state.subscribe(() => {
								const phase = input.state.getSnapshot().phase;
								if (phase === "submitting" || phase === "adjudicating") {
									hidePolish();
									onSubmit();
								}
							});
						},
						submitDraft: () => {
							inputFace()?.submit();
						},
						notifyVoiceActive: (active) => {
							remote.jxSpeech.setVoiceActive(active).then((result) => {
								if (result !== void 0 && !result.ok) throw new Error(result.error.message);
							}).catch(() => void 0);
						},
						transcribe: async (audioBase64, mimeType) => unwrap(await remote.jxSpeech.transcribe(audioBase64, mimeType), "jxSpeech.transcribe").text,
						transcribePcm: async (pcmBase64) => unwrap(await remote.jxSpeech.transcribePcm(pcmBase64), "jxSpeech.transcribePcm").text,
						localEngine: () => currentSettings().localStt && localReady,
						hotkey: () => currentSettings().hotkey,
						hotkeyMode: () => currentSettings().hotkeyMode,
						onVoiceStart: (start) => {
							showPolish(start);
						},
						onVoiceEnd: (start, chars) => {
							if (chars <= 0) {
								hidePolish();
								return;
							}
							showPolish(start);
						},
						notifyError,
						t: child.locale.bind(NS)
					};
				}
			}, MicButton));
			child.slots.inject("conversation.input.overlay", () => child.slots.register({
				name: "conversation.input.overlay",
				id: "dsh-speech-polish",
				order: 0,
				locale: NS,
				inject: (sessionId) => {
					const inputFace = () => inputFaceOf(sessionId);
					const t = (key) => child.locale.bind(NS)(key);
					return {
						hooks: { polish: polishSource },
						polish: async (context) => {
							if (voiceStart === null) return;
							const input = inputFace();
							if (input === void 0) return;
							setPolishState({
								visible: true,
								busy: true,
								error: ""
							});
							try {
								const draft = input.state.getSnapshot().draft;
								const start = Math.min(voiceStart, draft.length);
								const text = draft.slice(start);
								if (text.trim() === "") {
									hidePolish();
									return;
								}
								const result = unwrap(await remote.jxSpeech.polish(text, context), "jxSpeech.polish").text;
								input.setDraft(draft.slice(0, start) + result);
								hidePolish();
							} catch (error) {
								const message = error instanceof Error ? error.message : String(error);
								setPolishState({
									visible: true,
									busy: false,
									error: `${t("polish.failed")}: ${message.slice(0, 160)}`
								});
								setTimeout(() => {
									setPolishState((previous) => previous.error !== "" ? {
										visible: true,
										busy: false,
										error: ""
									} : previous);
								}, 8e3);
							}
						},
						dismiss: hidePolish,
						useContext: () => currentSettings().polishContext,
						t
					};
				}
			}, PolishBubble));
		}
	});
	return async () => {
		await disposeUI();
		await disposeMount();
	};
}
//#endregion
exports.apply = apply;
exports.inject = inject;

return module.exports; } });
//# sourceMappingURL=client.js.map