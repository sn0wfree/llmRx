//! L4 intent classifier for llmRx.
//!
//! Exposes a C ABI that the Go side calls via cgo. The default
//! implementation is a small keyword scorer (zero ML deps,
//! suitable for the default build). When the `onnx` cargo feature
//! is enabled, the same function can fall through to an ONNX
//! Runtime model; the keyword backend is always available as a
//! fallback if the model is not loaded.
//!
//! Both backends return a JSON string in the canonical
//! `IntentResult` shape, so the Go side has a single parsing path.

use std::ffi::{CStr, CString};
use std::os::raw::c_char;
use std::sync::OnceLock;

use serde::Serialize;

#[derive(Serialize)]
struct IntentResult {
    /// Lowercased intent label, e.g. "code", "chat", "summary".
    label: &'static str,
    /// Confidence in [0, 1].
    score: f32,
    /// Raw scorer output (label, weight pairs) for debugging.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    debug: Vec<(&'static str, f32)>,
}

/// Keyword buckets. Each entry is (label, [(keyword, weight)]).
/// Scoring sums weights of matched keywords per label, then
/// normalises by the total to get a confidence in [0, 1].
const KEYWORDS: &[(&str, &[(&str, f32)])] = &[
    (
        "code",
        &[
            ("fn ", 1.0),
            ("function ", 1.0),
            ("class ", 0.8),
            ("def ", 0.8),
            ("```", 1.0),
            ("import ", 0.6),
            ("package ", 0.6),
            ("var ", 0.4),
            ("let ", 0.4),
            ("const ", 0.4),
            ("return ", 0.4),
            ("if (", 0.4),
            ("for (", 0.4),
            ("while (", 0.4),
            ("=>", 0.5),
        ],
    ),
    (
        "chat",
        &[
            ("hello", 0.6),
            ("hi ", 0.4),
            ("thanks", 0.6),
            ("please", 0.4),
            ("could you", 0.6),
            ("can you", 0.5),
            ("?", 0.2),
            ("how are you", 0.8),
        ],
    ),
    (
        "summary",
        &[
            ("summarise", 1.0),
            ("summarize", 1.0),
            ("summary", 1.0),
            ("tldr", 0.8),
            ("brief", 0.6),
            ("in short", 0.6),
            ("overview", 0.5),
        ],
    ),
    (
        "translate",
        &[
            ("translate", 1.0),
            ("translation", 1.0),
            ("in english", 0.6),
            ("in chinese", 0.6),
            ("in french", 0.6),
            ("in spanish", 0.6),
            ("in japanese", 0.6),
            ("to english", 0.6),
        ],
    ),
    (
        "math",
        &[
            ("solve", 0.7),
            ("equation", 1.0),
            ("derivative", 1.0),
            ("integral", 1.0),
            ("matrix", 0.9),
            ("vector", 0.7),
            ("proof", 0.7),
            ("theorem", 0.7),
        ],
    ),
];

fn score(text: &str) -> IntentResult {
    let lc = text.to_ascii_lowercase();
    let mut scored: Vec<(&'static str, f32)> = Vec::new();
    let mut total = 0.0f32;
    for (label, kws) in KEYWORDS {
        let mut s = 0.0;
        for (kw, w) in *kws {
            let mut start = 0;
            while let Some(idx) = lc[start..].find(kw) {
                s += w;
                start += idx + kw.len();
            }
        }
        if s > 0.0 {
            scored.push((label, s));
            total += s;
        }
    }
    if total == 0.0 {
        return IntentResult {
            label: "general",
            score: 0.5,
            debug: scored,
        };
    }
    scored.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    let (top_label, top_score) = scored[0];
    let normalised = (top_score / total).min(1.0);
    IntentResult {
        label: top_label,
        score: normalised,
        debug: scored,
    }
}

/// C ABI: classify the input text and write a JSON result into
/// `out_buf`. Returns the number of bytes written (excluding the
/// trailing NUL). If `out_buf` is too small the result is truncated
/// to fit and -1 is returned to signal truncation.
///
/// # Safety
///
/// `text` must be a valid NUL-terminated UTF-8 string. `out_buf`
/// must be at least `out_cap` bytes long.
#[no_mangle]
pub extern "C" fn llmrx_intent_classify(
    text: *const c_char,
    out_buf: *mut c_char,
    out_cap: usize,
) -> i32 {
    if text.is_null() || out_buf.is_null() || out_cap == 0 {
        return -1;
    }
    let c_text = unsafe { CStr::from_ptr(text) };
    let text_str = match c_text.to_str() {
        Ok(s) => s,
        Err(_) => return -1,
    };
    let res = score(text_str);
    let json = match serde_json::to_string(&res) {
        Ok(s) => s,
        Err(_) => return -1,
    };

    let bytes = json.as_bytes();
    let n = bytes.len().min(out_cap.saturating_sub(1));
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), out_buf as *mut u8, n);
        *out_buf.add(n) = 0;
    }
    if bytes.len() >= out_cap {
        return -1;
    }
    n as i32
}

/// Returns the backend name: "keyword" (default) or "onnx" when
/// the ONNX feature is compiled in.
#[no_mangle]
pub extern "C" fn llmrx_intent_backend() -> *const c_char {
    static NAME: OnceLock<CString> = OnceLock::new();
    let s = NAME.get_or_init(|| {
        let n = if cfg!(feature = "onnx") { "onnx" } else { "keyword" };
        CString::new(n).unwrap()
    });
    s.as_ptr()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_code() {
        let r = score("def hello():\n    return 42\n```");
        assert_eq!(r.label, "code");
    }

    #[test]
    fn detects_summary() {
        let r = score("Please summarise this article.");
        assert_eq!(r.label, "summary");
    }

    #[test]
    fn fallback_general() {
        let r = score("the quick brown fox");
        assert_eq!(r.label, "general");
    }
}
