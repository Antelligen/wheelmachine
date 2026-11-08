/** The Wheelmachine API, from TypeScript and JavaScript: cloud machines for people and agents, paid by the second. */

export { Wheelmachine, DEFAULT_BASE_URL } from "./client.js";
export type { ClientOptions } from "./client.js";
export { DeviceLogin } from "./login.js";
export type { DeviceLoginOptions } from "./login.js";
export {
  WheelmachineError,
  APIConnectionError,
  APITimeoutError,
  BadRequestError,
  AuthenticationError,
  PaymentRequiredError,
  PermissionDeniedError,
  NotFoundError,
  WaitTimeoutError,
  ConflictError,
  RateLimitError,
  InternalServerError,
  UnavailableError,
} from "./errors.js";
export type { ErrorFields } from "./errors.js";
export type { FileData } from "./files.js";
export { PageIterator } from "./pagination.js";
export { Machines, pictureBytes } from "./resources/machines.js";
export { Ports } from "./resources/ports.js";
export { Deployments } from "./resources/deployments.js";
export { Apps } from "./resources/apps.js";
export { Projects } from "./resources/projects.js";
export { ConfirmCodes, Keys } from "./resources/keys.js";
export { Secrets, SECRET_VALUE_MAX } from "./resources/secrets.js";
export { Agents, Models } from "./resources/models.js";
export { Orgs } from "./resources/orgs.js";
export { Requests } from "./resources/requests.js";
export { Audit } from "./resources/audit.js";
export { Credit } from "./resources/credit.js";
export { VERSION } from "./version.js";
export * from "./types.js";

import { Wheelmachine } from "./client.js";
export default Wheelmachine;
