// Package maps serves the offline maps of the node (companion-services.md section 4.4): the best
// verified pmtiles copy of each map with HTTP Range, the local style assets (fonts and sprites), the
// /maps/catalog.json that tells the viewer what exists, and the health probe of the "maps" service.
// Nothing here talks to the internet; everything comes from the ark index's best copies.
package maps
