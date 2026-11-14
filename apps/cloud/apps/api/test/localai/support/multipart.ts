// Test support: a multipart/form-data body for `app.inject` (text fields and at most one file), built by hand so the
// test controls every byte (an oversized file, a duplicate field, a file under the wrong name).
export interface MultipartFile {
  field: string;
  filename: string;
  contentType: string;
  bytes: Buffer;
}

export function multipartBody(fields: Record<string, string>, file?: MultipartFile, extraFields: Array<[string, string]> = []): { payload: Buffer; headers: Record<string, string> } {
  const boundary = `----music-test-${Math.random().toString(16).slice(2)}`;
  const parts: Buffer[] = [];
  for (const [name, value] of [...Object.entries(fields), ...extraFields]) {
    parts.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${name}"\r\n\r\n${value}\r\n`, 'utf8'));
  }
  if (file) {
    parts.push(Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="${file.field}"; filename="${file.filename}"\r\nContent-Type: ${file.contentType}\r\n\r\n`, 'utf8'));
    parts.push(file.bytes);
    parts.push(Buffer.from('\r\n', 'utf8'));
  }
  parts.push(Buffer.from(`--${boundary}--\r\n`, 'utf8'));
  return { payload: Buffer.concat(parts), headers: { 'content-type': `multipart/form-data; boundary=${boundary}` } };
}

/** A minimal WAV file of `size` bytes: the RIFF/WAVE header, then silence. */
export function wavBytes(size = 4096): Buffer {
  const bytes = Buffer.alloc(Math.max(size, 44));
  bytes.write('RIFF', 0, 'ascii');
  bytes.writeUInt32LE(bytes.length - 8, 4);
  bytes.write('WAVE', 8, 'ascii');
  bytes.write('fmt ', 12, 'ascii');
  return bytes;
}
