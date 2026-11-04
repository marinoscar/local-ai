/* global AudioWorkletProcessor, registerProcessor */
// AudioWorklet that hands raw microphone samples (mono, the first channel) to the page. It is the
// fallback of the Talk recorder for browsers without MediaRecorder (survival-portal.md section 4.5
// step 2). Served from this origin as a bundled asset, so the Portal CSP (script-src 'self') allows it.
class PcmCapture extends AudioWorkletProcessor {
  process(inputs) {
    const ch = inputs[0] && inputs[0][0];
    if (ch && ch.length > 0) {
      this.port.postMessage(ch.slice(0));
    }
    return true;
  }
}
registerProcessor("pcm-capture", PcmCapture);
