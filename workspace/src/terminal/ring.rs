//! A fixed-size byte ring: the terminal scrollback that a reconnecting
//! client replays onto a reset screen.

const CAP: usize = 256 * 1024;

pub struct Ring {
    buf: Vec<u8>,
}

impl Ring {
    pub fn new() -> Self {
        Self {
            buf: Vec::with_capacity(CAP),
        }
    }

    pub fn push(&mut self, bytes: &[u8]) {
        if bytes.len() >= CAP {
            self.buf.clear();
            self.buf.extend_from_slice(&bytes[bytes.len() - CAP..]);
            return;
        }
        self.buf.extend_from_slice(bytes);
        if self.buf.len() > CAP {
            let overflow = self.buf.len() - CAP;
            self.buf.drain(..overflow);
        }
    }

    pub fn snapshot(&self) -> Vec<u8> {
        self.buf.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn appends_in_order_under_cap() {
        let mut r = Ring::new();
        r.push(b"hello ");
        r.push(b"world");
        assert_eq!(r.snapshot(), b"hello world");
    }

    #[test]
    fn evicts_oldest_past_cap() {
        let mut r = Ring::new();
        r.push(&vec![b'a'; CAP - 3]);
        r.push(b"XYZ");
        let s = r.snapshot();
        assert_eq!(s.len(), CAP);
        assert_eq!(&s[s.len() - 3..], b"XYZ");
        assert_eq!(s[0], b'a');
    }

    #[test]
    fn a_single_write_larger_than_cap_keeps_only_the_tail() {
        let mut r = Ring::new();
        let mut big = vec![b'.'; CAP];
        big.extend_from_slice(b"TAIL");
        r.push(&big);
        let s = r.snapshot();
        assert_eq!(s.len(), CAP);
        assert_eq!(&s[s.len() - 4..], b"TAIL");
    }

    #[test]
    fn empty_ring_snapshots_empty() {
        assert!(Ring::new().snapshot().is_empty());
    }

    #[test]
    fn many_small_writes_evict_from_the_front() {
        let mut r = Ring::new();
        r.push(&vec![b'a'; CAP - 2]);
        r.push(b"bcd");
        let s = r.snapshot();
        assert_eq!(s.len(), CAP);
        assert_eq!(s[0], b'a');
        assert_eq!(&s[s.len() - 3..], b"bcd");
        assert_eq!(s.iter().filter(|&&b| b == b'a').count(), CAP - 3);
    }
}
