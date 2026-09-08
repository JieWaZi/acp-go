// Fixed upstream source modules; no user-configurable auto-approval entry point.
export { createMcpAdapter } from './node_modules/pi-mcp-adapter/index.ts';
export { classifyAction } from './node_modules/pi-auto-approval/src/classifier.ts';
export { createReviewSubject } from './node_modules/pi-auto-approval/src/tool-routing.ts';
export { completeSimple } from '@earendil-works/pi-ai';
