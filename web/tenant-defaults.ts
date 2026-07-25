// Build-time default for the catalog CSP's frame-ancestors (see
// next.config.ts for what it governs and why it is build-time). 'self' means
// only arti itself may iframe its catalog/viewer pages; to let another
// trusted origin embed the viewer, set ARTI_CATALOG_FRAME_ANCESTORS as a
// build argument when building the web image.
export const DEFAULT_CATALOG_FRAME_ANCESTORS = "'self'";
