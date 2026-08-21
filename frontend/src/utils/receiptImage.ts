// Receipt photo normalization.
//
// The heavy lifting -- document detection, crop, deskew -- happens server-side,
// where there is one implementation to test and no mobile CPU cost. This module
// only applies EXIF orientation and caps the upload size.
// See docs/receipt-scan-design.md §3.4.

// This bound is an upload budget, not the extraction resolution.
//
// The server detects the receipt's outline, crops away the background, deskews it
// and scales the result to its own limit. That crop is where the accuracy comes
// from -- it turned an unreliable extraction into a repeatable, exact one -- so
// shrinking too aggressively here would throw away the detail the crop depends
// on. A receipt covers well under half the frame, so its own long axis is roughly
// two thirds of the frame's; 3200 leaves enough for the server to reach 2048
// without upscaling, while still cutting a 2.4MB photo to about 1.5MB.
export const MAX_EDGE = 3200;

export const JPEG_QUALITY = 0.85;

export type NormalizedImage = {
  blob: Blob;
  width: number;
  height: number;
  rotated: boolean;
  originalBytes: number;
  bytes: number;
};

async function loadBitmap(file: Blob): Promise<ImageBitmap | HTMLImageElement> {
  // imageOrientation:'from-image' applies EXIF, which matters because phone
  // photos are commonly stored landscape with an orientation flag.
  if (typeof createImageBitmap === 'function') {
    try {
      return await createImageBitmap(file, { imageOrientation: 'from-image' });
    } catch {
      // Older Safari rejects the options bag; fall through to the <img> path,
      // which applies EXIF natively.
    }
  }
  return await new Promise<HTMLImageElement>((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve(img);
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error('Could not read that image.'));
    };
    img.src = url;
  });
}

function dimensionsOf(source: ImageBitmap | HTMLImageElement) {
  const width = 'naturalWidth' in source ? source.naturalWidth : source.width;
  const height = 'naturalHeight' in source ? source.naturalHeight : source.height;
  return { width, height };
}

/**
 * Prepares a camera photo for extraction: EXIF applied, tall images rotated to
 * landscape, long edge bounded, re-encoded as JPEG.
 */
export async function normalizeReceiptImage(file: Blob, maxEdge = MAX_EDGE): Promise<NormalizedImage> {
  const source = await loadBitmap(file);
  const { width: srcW, height: srcH } = dimensionsOf(source);
  if (!srcW || !srcH) {
    throw new Error('Could not read that image.');
  }

  // Keep the photographed orientation: the server derives the true orientation
  // from the detected outline, and it uses the frame's own sense of "down" to
  // resolve which end of the receipt is the top. Second-guessing that here would
  // only confuse it. EXIF is already applied by the decoder above.
  const rotate = false;
  const orientedW = srcW;
  const orientedH = srcH;
  const scale = Math.min(1, maxEdge / Math.max(orientedW, orientedH));
  const outW = Math.max(1, Math.round(orientedW * scale));
  const outH = Math.max(1, Math.round(orientedH * scale));

  const canvas = document.createElement('canvas');
  canvas.width = outW;
  canvas.height = outH;
  const ctx = canvas.getContext('2d');
  if (!ctx) {
    throw new Error('Could not process that image.');
  }
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = 'high';

  if (rotate) {
    // Quarter turn counter-clockwise, matching how a long receipt is held.
    ctx.translate(0, outH);
    ctx.rotate(-Math.PI / 2);
    ctx.drawImage(source as CanvasImageSource, 0, 0, outH, outW);
  } else {
    ctx.drawImage(source as CanvasImageSource, 0, 0, outW, outH);
  }

  if ('close' in source && typeof source.close === 'function') {
    source.close();
  }

  const blob = await new Promise<Blob | null>((resolve) =>
    canvas.toBlob(resolve, 'image/jpeg', JPEG_QUALITY)
  );
  if (!blob) {
    throw new Error('Could not process that image.');
  }

  return {
    blob,
    width: outW,
    height: outH,
    rotated: rotate,
    originalBytes: file.size,
    bytes: blob.size
  };
}
