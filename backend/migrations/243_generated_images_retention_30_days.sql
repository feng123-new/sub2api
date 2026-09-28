UPDATE generated_images
SET expires_at = created_at + INTERVAL '30 days'
WHERE expires_at > NOW()
  AND expires_at < created_at + INTERVAL '30 days';
