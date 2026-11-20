-- Chat moves out of the zero value; all other Action types retain their IDs.
UPDATE actions SET type = 14 WHERE type = 0;
