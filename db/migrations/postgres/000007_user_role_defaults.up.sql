ALTER TABLE users
  ALTER COLUMN role SET DEFAULT 'user';

UPDATE users 
  SET role = 'user' 
  WHERE role = 'admin' AND email NOT IN (
    SELECT email FROM users 
    WHERE role = 'admin' 
    LIMIT 1
  );
