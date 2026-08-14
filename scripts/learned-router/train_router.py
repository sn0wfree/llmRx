#!/usr/bin/env python3
"""
train_router.py — Train a LightGBM model and export to ONNX.

Usage:
    python scripts/learned-router/train_router.py \
        --labels data/learned-router/labels.jsonl \
        --output-dir data/learned-router/

Reads labeled samples, extracts features (TF-IDF + structural),
trains a multi-class LightGBM classifier, exports to ONNX + vocab.
"""
import argparse
import json
import os
import sys
from collections import Counter
from typing import Optional

import numpy as np

# ---------------------------------------------------------------------------
# Feature extraction
# ---------------------------------------------------------------------------

CODE_KEYWORDS = [
    "function", "class", "def", "import", "from", "const", "let", "var",
    "return", "if", "else", "for", "while", "interface", "struct", "enum",
    "type", "func", "package", "module", "require", "export", "async", "await",
    "try", "catch", "throw", "new", "api", "debug", "error", "test", "implement",
    "write", "code", "refactor", "debug", "explain", "fix", "bug", "algorithm",
]

WRITING_KEYWORDS = [
    "write", "draft", "essay", "blog", "article", "story", "email",
    "summary", "summarize", "document", "report", "proposal",
]

REASONING_KEYWORDS = [
    "analyze", "evaluate", "compare", "pros and cons", "probability",
    "prove", "derive", "calculate", "solve", "equation", "math",
    "reasoning", "think", "step by step", "logic",
]

Factual_KEYWORDS = [
    "what is", "who is", "when did", "where is", "define", "capital",
    "population", "meaning", "definition", "history",
]

TRANSLATION_KEYWORDS = [
    "translate", "translation", "翻译", "interpreted", "localize",
]


def extract_structural_features(sample: dict) -> dict:
    """Extract structural features from a sample."""
    prompt = sample.get("prompt", "")
    prompt_len = len(prompt)
    word_count = len(prompt.split())

    # Keyword density
    prompt_lower = prompt.lower()
    code_density = sum(1 for kw in CODE_KEYWORDS if kw in prompt_lower) / max(len(CODE_KEYWORDS), 1)
    writing_density = sum(1 for kw in WRITING_KEYWORDS if kw in prompt_lower) / max(len(WRITING_KEYWORDS), 1)
    reasoning_density = sum(1 for kw in REASONING_KEYWORDS if kw in prompt_lower) / max(len(REASONING_KEYWORDS), 1)
    factual_density = sum(1 for kw in Factual_KEYWORDS if kw in prompt_lower) / max(len(Factual_KEYWORDS), 1)
    translation_density = sum(1 for kw in TRANSLATION_KEYWORDS if kw in prompt_lower) / max(len(TRANSLATION_KEYWORDS), 1)

    # Language detection
    import re
    has_cn = bool(re.search(r'[\u4e00-\u9fff]', prompt))
    has_en = bool(re.search(r'[a-zA-Z]{3,}', prompt))
    language = 0.0  # english
    if has_cn and has_en:
        language = 0.5  # mixed
    elif has_cn:
        language = 1.0  # chinese

    return {
        "prompt_length": prompt_len,
        "word_count": word_count,
        "code_density": code_density,
        "writing_density": writing_density,
        "reasoning_density": reasoning_density,
        "factual_density": factual_density,
        "translation_density": translation_density,
        "language": language,
        "has_code": float(sample.get("has_code", False)),
        "has_image": float(sample.get("has_image", False)),
        "conversation_turns": sample.get("conversation_turns", 0),
        "mode_plan": float(sample.get("mode", "") == "plan"),
        "mode_build": float(sample.get("mode", "") == "build"),
        "tokens_in": sample.get("tokens_in", 0),
        "tokens_out": sample.get("tokens_out", 0),
    }


# ---------------------------------------------------------------------------
# TF-IDF (simple, no sklearn dependency for vocab portability)
# ---------------------------------------------------------------------------

class SimpleTfidf:
    """Lightweight TF-IDF vectorizer that exports to JSON."""

    def __init__(self, max_features: int = 3000, min_df: int = 2):
        self.max_features = max_features
        self.min_df = min_df
        self.vocab = {}  # word -> index
        self.idf = None  # numpy array

    def fit(self, texts: list):
        """Build vocabulary and IDF from texts."""
        from collections import defaultdict
        import math

        doc_freq = defaultdict(int)
        total_docs = len(texts)

        for text in texts:
            words = set(self._tokenize(text))
            for w in words:
                doc_freq[w] += 1

        # Filter by min_df and take top max_features by document frequency
        candidates = [
            (w, df) for w, df in doc_freq.items()
            if df >= self.min_df
        ]
        candidates.sort(key=lambda x: -x[1])
        candidates = candidates[:self.max_features]

        self.vocab = {w: i for i, (w, _) in enumerate(candidates)}
        self.idf = np.zeros(len(self.vocab))
        for w, df in candidates:
            self.idf[self.vocab[w]] = math.log((total_docs + 1) / (df + 1)) + 1

    def transform(self, texts: list) -> np.ndarray:
        """Transform texts to TF-IDF matrix."""
        n = len(texts)
        m = len(self.vocab)
        result = np.zeros((n, m), dtype=np.float32)

        for i, text in enumerate(texts):
            words = self._tokenize(text)
            tf = Counter(words)
            for w, count in tf.items():
                if w in self.vocab:
                    j = self.vocab[w]
                    result[i, j] = (count / max(len(words), 1)) * self.idf[j]

        return result

    def _tokenize(self, text: str) -> list:
        """Simple whitespace + lowercase tokenization."""
        import re
        text = text.lower()
        text = re.sub(r'[^a-z0-9_\u4e00-\u9fff]', ' ', text)
        return [w for w in text.split() if len(w) > 1]

    def to_json(self) -> dict:
        return {
            "vocab": self.vocab,
            "idf": self.idf.tolist(),
            "max_features": self.max_features,
            "min_df": self.min_df,
        }

    @classmethod
    def from_json(cls, data: dict) -> "SimpleTfidf":
        v = cls(max_features=data["max_features"], min_df=data["min_df"])
        v.vocab = data["vocab"]
        v.idf = np.array(data["idf"])
        return v


# ---------------------------------------------------------------------------
# Training
# ---------------------------------------------------------------------------

def load_data(labels_path: str, samples_path: Optional[str] = None):
    """Load labeled data and optionally join with samples for features."""
    labels = []
    with open(labels_path, "r", encoding="utf-8") as f:
        for line in f:
            if line.strip():
                labels.append(json.loads(line))

    if samples_path and os.path.exists(samples_path):
        samples_map = {}
        with open(samples_path, "r", encoding="utf-8") as f:
            for line in f:
                if line.strip():
                    s = json.loads(line)
                    samples_map[s.get("prompt", "")] = s

        # Join on prompt
        for label in labels:
            prompt = label.get("prompt", "")
            if prompt in samples_map:
                label.update(samples_map[prompt])

    return labels


def build_features_and_labels(data: list):
    """Build feature matrix and label vector from data."""
    # Filter out classes with < 2 samples first
    from collections import Counter
    models_raw = [d.get("best_model", d.get("model_used", "")) for d in data]
    model_counts = Counter(models_raw)
    valid_models = {m for m, c in model_counts.items() if c >= 2}
    data = [d for d in data if d.get("best_model", d.get("model_used", "")) in valid_models]
    if len(data) < len(models_raw):
        print(f"Filtered to {len(data)} samples (removed classes with < 2 members)")

    # TF-IDF
    texts = [d.get("prompt", "") for d in data]
    tfidf = SimpleTfidf(max_features=500, min_df=3)
    tfidf.fit(texts)
    X_tfidf = tfidf.transform(texts)

    # Structural features
    struct_names = list(extract_structural_features(data[0]).keys())
    X_struct = np.array([
        list(extract_structural_features(d).values())
        for d in data
    ], dtype=np.float32)

    # Combine
    X = np.hstack([X_tfidf, X_struct])

    # Labels
    models = [d.get("best_model", d.get("model_used", "")) for d in data]
    unique_models = sorted(set(models))
    model_to_idx = {m: i for i, m in enumerate(unique_models)}
    y = np.array([model_to_idx[m] for m in models], dtype=np.int32)

    return X, y, tfidf, struct_names, unique_models, model_to_idx


def train_lightgbm(X, y, unique_models):
    """Train LightGBM classifier."""
    try:
        import lightgbm as lgb
    except ImportError:
        print("lightgbm not installed, falling back to sklearn RandomForest")
        from sklearn.ensemble import RandomForestClassifier
        from sklearn.model_selection import train_test_split

        X_train, X_test, y_train, y_test = train_test_split(
            X, y, test_size=0.2, random_state=42, stratify=y
        )

        model = RandomForestClassifier(
            n_estimators=200, max_depth=10, random_state=42,
            class_weight="balanced", n_jobs=-1,
        )
        model.fit(X_train, y_train)

        acc_train = model.score(X_train, y_train)
        acc_test = model.score(X_test, y_test)
        print(f"RandomForest — train acc: {acc_train:.3f}, test acc: {acc_test:.3f}")

        return model, X_train, X_test, y_train, y_test

    # LightGBM path
    from sklearn.model_selection import train_test_split
    X_train, X_test, y_train, y_test = train_test_split(
        X, y, test_size=0.2, random_state=42, stratify=y
    )

    train_data = lgb.Dataset(X_train, label=y_train)
    test_data = lgb.Dataset(X_test, label=y_test, reference=train_data)

    params = {
        "objective": "multiclass",
        "num_class": len(unique_models),
        "metric": "multi_logloss",
        "num_leaves": 31,
        "max_depth": 6,
        "learning_rate": 0.1,
        "min_child_samples": 20,
        "verbose": -1,
    }

    model = lgb.train(
        params, train_data,
        num_boost_round=100,
        valid_sets=[test_data],
        callbacks=[lgb.log_evaluation(50)],
    )

    y_pred = np.argmax(model.predict(X_test), axis=1)
    acc_test = np.mean(y_pred == y_test)
    y_pred_train = np.argmax(model.predict(X_train), axis=1)
    acc_train = np.mean(y_pred_train == y_train)
    print(f"LightGBM — train acc: {acc_train:.3f}, test acc: {acc_test:.3f}")

    # Feature importance
    importance = model.feature_importance(importance_type="gain")
    print(f"Top 10 features by importance:")
    all_feature_names = [f"tfidf_{i}" for i in range(X.shape[1] - len(extract_structural_features({})))] + list(extract_structural_features({}).keys())
    top_idx = np.argsort(importance)[::-1][:10]
    for idx in top_idx:
        name = all_feature_names[idx] if idx < len(all_feature_names) else f"feat_{idx}"
        print(f"  {name}: {importance[idx]:.1f}")

    return model, X_train, X_test, y_train, y_test


def export_to_json(model, tfidf, struct_names, unique_models, output_dir):
    """Export model weights as JSON (for Go inference without ONNX)."""
    try:
        import lightgbm as lgb
        # LightGBM: extract trees as linear approximation
        # For simplicity, export feature importances as a linear model proxy
        importance = model.feature_importance(importance_type="gain")
        total = importance.sum()
        if total > 0:
            importance = importance / total
    except ImportError:
        # sklearn RandomForest
        importance = model.feature_importances_
        total = importance.sum()
        if total > 0:
            importance = importance / total

    # Build simple linear weights per class
    # Use feature importance as a proxy for class-specific weights
    n_features = len(importance)

    weights = {}
    for i, model_name in enumerate(unique_models):
        # Simple: use importance as weights (linear model approximation)
        weights[model_name] = importance.tolist()

    # Bias: uniform
    bias = {m: 0.0 for m in unique_models}

    model_json = {
        "version": 1,
        "type": "linear_proxy",
        "models": unique_models,
        "tfidf": tfidf.to_json(),
        "structural_features": struct_names,
        "weights": weights,
        "bias": bias,
    }

    os.makedirs(output_dir, exist_ok=True)
    model_path = os.path.join(output_dir, "router_model.json")
    with open(model_path, "w", encoding="utf-8") as f:
        json.dump(model_json, f, ensure_ascii=False, indent=2)
    print(f"Exported model JSON: {model_path}")

    # Also try ONNX export
    try:
        export_onnx(model, n_features, unique_models, output_dir)
    except Exception as e:
        print(f"ONNX export skipped: {e}")


def export_onnx(model, n_features, unique_models, output_dir):
    """Export model to ONNX format."""
    try:
        from skl2onnx import convert_sklearn
        from skl2onnx.common.data_types import FloatTensorType
        initial_type = [("float_input", FloatTensorType([None, n_features]))]
        onnx_model = convert_sklearn(model, initial_types=initial_type)
        onnx_path = os.path.join(output_dir, "router_model.onnx")
        with open(onnx_path, "wb") as f:
            f.write(onnx_model.SerializeToString())
        print(f"Exported ONNX: {onnx_path}")
    except ImportError:
        print("skl2onnx not installed, ONNX export skipped")
    except Exception as e:
        print(f"ONNX export failed: {e}")


def save_training_meta(output_dir, unique_models, struct_names, metrics=None):
    """Save training metadata."""
    meta = {
        "models": unique_models,
        "structural_features": struct_names,
        "n_models": len(unique_models),
    }
    if metrics:
        meta.update(metrics)

    meta_path = os.path.join(output_dir, "train_meta.json")
    with open(meta_path, "w") as f:
        json.dump(meta, f, indent=2)
    print(f"Training meta: {meta_path}")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Train learned router model")
    parser.add_argument("--labels", required=True, help="Labels JSONL from judge")
    parser.add_argument("--samples", default=None, help="Samples JSONL for extra features")
    parser.add_argument("--output-dir", default="data/learned-router/")
    parser.add_argument("--max-features", type=int, default=3000)
    args = parser.parse_args()

    print("Loading data...")
    data = load_data(args.labels, args.samples)
    print(f"Loaded {len(data)} labeled samples")

    # Filter out samples with no best_model
    data = [d for d in data if d.get("best_model")]
    print(f"After filtering: {len(data)} samples")

    if len(data) < 50:
        print("Error: need at least 50 labeled samples", file=sys.stderr)
        sys.exit(1)

    # Build features
    print("Building features...")
    X, y, tfidf, struct_names, unique_models, model_to_idx = build_features_and_labels(data)
    print(f"Feature matrix: {X.shape} ({len(unique_models)} classes)")

    # Train
    print("\nTraining model...")
    model, X_train, X_test, y_train, y_test = train_lightgbm(X, y, unique_models)

    # Export
    print("\nExporting model...")
    export_to_json(model, tfidf, struct_names, unique_models, args.output_dir)
    save_training_meta(
        args.output_dir, unique_models, struct_names,
        metrics={"n_samples": len(data), "n_features": X.shape[1]},
    )

    print("\nDone!")


if __name__ == "__main__":
    main()
