//! Shared-byte-corpus test. The same files under
//! `internal/transport/testdata/framing` are decoded by the Go server
//! (`internal/transport/framing_fixture_test.go`) and the Android client
//! (`FramingFixtures.java`); `docs/protocol-framing.md` describes the layout.

use std::fs;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

use anyhow::Error;
use rdev_client_rs::{protocol, stream_frame};

fn fixture_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../internal/transport/testdata/framing")
}

fn hex_decode(text: &str) -> Vec<u8> {
    assert!(text.len() % 2 == 0, "odd hex length: {text}");
    (0..text.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&text[i..i + 2], 16).expect("hex"))
        .collect()
}

struct StreamRow {
    name: String,
    file: String,
    expect: String,
    kind: u8,
    payload: Vec<u8>,
    remaining: Vec<u8>,
}

struct InnerRow {
    name: String,
    file: String,
    expect: String,
    typ: u8,
    id: String,
    offseted: bool,
    offset: i64,
    payload: Vec<u8>,
}

fn load() -> (Vec<StreamRow>, Vec<InnerRow>) {
    let text = fs::read_to_string(fixture_root().join("fixtures.tsv")).expect("fixtures.tsv");
    let mut streams = Vec::new();
    let mut inners = Vec::new();
    let mut section = "";
    for line in text.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if line == "[stream]" {
            section = "stream";
            continue;
        }
        if line == "[inner]" {
            section = "inner";
            continue;
        }
        let fields: Vec<&str> = line.split('\t').collect();
        match section {
            "stream" => {
                assert_eq!(fields.len(), 6, "stream row {line:?}");
                let ok = fields[2] == "ok";
                streams.push(StreamRow {
                    name: fields[0].into(),
                    file: fields[1].into(),
                    expect: fields[2].into(),
                    kind: if ok {
                        fields[3].parse().expect("kind")
                    } else {
                        0
                    },
                    payload: if ok {
                        hex_decode(fields[4])
                    } else {
                        Vec::new()
                    },
                    remaining: if ok {
                        hex_decode(fields[5])
                    } else {
                        Vec::new()
                    },
                });
            }
            "inner" => {
                assert_eq!(fields.len(), 8, "inner row {line:?}");
                let ok = fields[2] == "ok";
                let offseted = ok && fields[5] == "1";
                inners.push(InnerRow {
                    name: fields[0].into(),
                    file: fields[1].into(),
                    expect: fields[2].into(),
                    typ: if ok {
                        fields[3].parse().expect("type")
                    } else {
                        0
                    },
                    id: fields[4].into(),
                    offseted,
                    offset: if offseted {
                        fields[6].parse().expect("offset")
                    } else {
                        0
                    },
                    payload: if ok {
                        hex_decode(fields[7])
                    } else {
                        Vec::new()
                    },
                });
            }
            _ => panic!("row outside a section: {line:?}"),
        }
    }
    assert!(!streams.is_empty() && !inners.is_empty(), "empty corpus");
    (streams, inners)
}

fn fixture_bytes(file: &str) -> Vec<u8> {
    fs::read(fixture_root().join(file)).unwrap_or_else(|err| panic!("read {file}: {err}"))
}

fn classify(err: &Error) -> &'static str {
    if err
        .downcast_ref::<std::io::Error>()
        .is_some_and(|e| e.kind() == ErrorKind::UnexpectedEof)
    {
        "eof"
    } else if err.to_string().contains("too large") {
        "too_large"
    } else {
        "other"
    }
}

#[tokio::test]
async fn stream_frames_match_fixture_corpus() {
    let (streams, _) = load();
    let mut index = 0;
    while index < streams.len() {
        let file = streams[index].file.clone();
        let data = fixture_bytes(&file);
        let mut reader: &[u8] = &data;
        while index < streams.len() && streams[index].file == file {
            let row = &streams[index];
            let last_for_file = index + 1 == streams.len() || streams[index + 1].file != row.file;
            let result = stream_frame::read_frame(&mut reader).await;
            if row.expect != "ok" {
                let err = result.expect_err("expected decode failure");
                let want = row.expect.strip_prefix("error:").unwrap_or("");
                assert_eq!(classify(&err), want, "{} ({})", row.name, row.file);
                assert!(last_for_file, "{}: failure before last row", row.name);
                index += 1;
                continue;
            }
            let frame = result.unwrap_or_else(|err| panic!("{} ({}): {err}", row.name, row.file));
            assert_eq!(frame.kind, row.kind, "{} ({})", row.name, row.file);
            assert_eq!(frame.payload, row.payload, "{} ({})", row.name, row.file);
            if last_for_file {
                assert_eq!(
                    reader,
                    &row.remaining[..],
                    "{} ({}) trailing",
                    row.name,
                    row.file
                );
            }
            index += 1;
        }
    }
}

#[test]
fn binary_frames_match_fixture_corpus() {
    let (_, inners) = load();
    for row in &inners {
        let raw = fixture_bytes(&row.file);
        if row.expect != "ok" {
            assert!(
                protocol::decode_bin_frame_offset(&raw).is_err(),
                "{} ({}): offset decoder accepted an invalid frame",
                row.name,
                row.file
            );
            continue;
        }
        let (typ, id, payload) = protocol::decode_bin_frame(&raw)
            .unwrap_or_else(|err| panic!("{} ({}): {err}", row.name, row.file));
        assert_eq!(typ, row.typ, "{} ({})", row.name, row.file);
        assert_eq!(id, row.id, "{} ({})", row.name, row.file);
        if row.offseted {
            let (otyp, oid, offset, opayload) = protocol::decode_bin_frame_offset(&raw)
                .unwrap_or_else(|err| panic!("{} ({}): {err}", row.name, row.file));
            assert_eq!(
                (otyp, oid.as_str(), offset),
                (row.typ, row.id.as_str(), row.offset),
                "{} ({})",
                row.name,
                row.file
            );
            assert_eq!(opayload, row.payload, "{} ({})", row.name, row.file);
            assert_eq!(
                raw,
                protocol::encode_bin_frame_offset(row.typ, &row.id, row.offset, &row.payload)
                    .unwrap(),
                "{} ({}): fixture bytes differ from encoder",
                row.name,
                row.file
            );
        } else {
            assert_eq!(payload, row.payload, "{} ({})", row.name, row.file);
            assert_eq!(
                raw,
                protocol::encode_bin_frame(row.typ, &row.id, &row.payload).unwrap(),
                "{} ({}): fixture bytes differ from encoder",
                row.name,
                row.file
            );
        }
    }
}
