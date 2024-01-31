use borsh::{BorshDeserialize, BorshSerialize};


#[derive(Clone, Copy, PartialEq, Eq, Debug, BorshSerialize, BorshDeserialize)]
pub struct Context([u8; Self::LEN]);

impl Context {
    pub const LEN: usize = 32;
    #[must_use]
    pub fn new(bytes: [u8; Self::LEN]) -> Self {
        Self(bytes)
    }
    #[must_use]
    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }
}
