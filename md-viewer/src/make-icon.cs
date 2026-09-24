using System;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Drawing.Imaging;
using System.IO;

// Build-time helper: convert a source PNG into a multi-size .ico whose every
// entry is a PNG-compressed image (Vista+). Compiled with the .NET Framework
// csc.exe so System.Drawing is available. Usage: MakeIcon.exe <source.png> <dest.ico>
internal static class MakeIcon
{
    private static int Main(string[] args)
    {
        if (args.Length < 2)
        {
            Console.Error.WriteLine("usage: MakeIcon.exe <source.png> <dest.ico>");
            return 1;
        }

        string sourcePath = args[0];
        string destPath = args[1];
        int[] sizes = new int[] { 16, 24, 32, 48, 64, 128, 256 };

        using (Bitmap source = new Bitmap(sourcePath))
        using (FileStream stream = new FileStream(destPath, FileMode.Create, FileAccess.Write))
        using (BinaryWriter writer = new BinaryWriter(stream))
        {
            byte[][] pngs = new byte[sizes.Length][];
            for (int i = 0; i < sizes.Length; i++)
            {
                pngs[i] = RenderPng(source, sizes[i]);
            }

            // ICONDIR
            writer.Write((ushort)0);                 // reserved
            writer.Write((ushort)1);                 // type: icon
            writer.Write((ushort)sizes.Length);      // image count

            // ICONDIRENTRY blocks; image data follows the table.
            int offset = 6 + 16 * sizes.Length;
            for (int i = 0; i < sizes.Length; i++)
            {
                int size = sizes[i];
                byte dim = (byte)(size >= 256 ? 0 : size);
                writer.Write(dim);                   // width (0 == 256)
                writer.Write(dim);                   // height (0 == 256)
                writer.Write((byte)0);               // palette count
                writer.Write((byte)0);               // reserved
                writer.Write((ushort)1);             // color planes
                writer.Write((ushort)32);            // bits per pixel
                writer.Write((uint)pngs[i].Length);  // image size
                writer.Write((uint)offset);          // image offset
                offset += pngs[i].Length;
            }

            foreach (byte[] png in pngs)
            {
                writer.Write(png);
            }
        }

        return 0;
    }

    private static byte[] RenderPng(Bitmap source, int size)
    {
        using (Bitmap target = new Bitmap(size, size, PixelFormat.Format32bppArgb))
        {
            using (Graphics g = Graphics.FromImage(target))
            {
                g.Clear(Color.Transparent);
                g.InterpolationMode = InterpolationMode.HighQualityBicubic;
                g.SmoothingMode = SmoothingMode.HighQuality;
                g.PixelOffsetMode = PixelOffsetMode.HighQuality;

                double scale = Math.Min((double)size / source.Width, (double)size / source.Height);
                int w = (int)Math.Round(source.Width * scale);
                int h = (int)Math.Round(source.Height * scale);
                int x = (size - w) / 2;
                int y = (size - h) / 2;
                g.DrawImage(
                    source,
                    new Rectangle(x, y, w, h),
                    new Rectangle(0, 0, source.Width, source.Height),
                    GraphicsUnit.Pixel);
            }

            using (MemoryStream ms = new MemoryStream())
            {
                target.Save(ms, ImageFormat.Png);
                return ms.ToArray();
            }
        }
    }
}
