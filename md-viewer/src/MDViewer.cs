using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.IO.Pipes;
using System.Linq;
using System.Runtime.InteropServices;
using System.Reflection;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Win32;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

// MD查看器 — single-exe Markdown viewer (Jianxiang Digital Assistant suite).
//
// Everything (WebView2 managed assemblies, the native loader, the viewer
// template) ships embedded as resources, so the product is ONE exe file.
// The first run registers the .md association automatically. Modes:
//   MD查看器.exe <file.md>    open the document (reuses the resident host)
//   MD查看器.exe              register + show the welcome note
//   MD查看器.exe /register    silent registration (installer task)
//   MD查看器.exe /unregister  silent unregistration (uninstaller)
//
// Process model: the first instance becomes the HOST (owns a named mutex),
// keeps the WebView2 environment warm and listens on a named pipe. Later
// instances forward their file path through the pipe and exit, so every
// window after the first opens instantly without respawning the browser
// process tree. When the last window closes the host either exits (default)
// or stays resident in the tray per the user setting ("keepRunning"),
// keeping a hidden WebView alive so the next double-click is instant.
internal static class MDViewerProgram
{
    internal const string AppName = "MD查看器";
    internal const string PipeName = "JianxiangMdViewerPipe";
    internal const string MutexName = @"Local\JianxiangMdViewerHost";
    internal static readonly string UserDataFolder = Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
        "Jianxiang", "MdViewer");

    [STAThread]
    private static void Main(string[] args)
    {
        // Embedded managed assemblies must resolve from resources before any
        // WebView2 type is touched; the native loader needs a on-disk copy.
        AppDomain.CurrentDomain.AssemblyResolve += OnAssemblyResolve;
        EnsureLoaderDll();

        if (args.Length > 0 && string.Equals(args[0], "/register", StringComparison.OrdinalIgnoreCase))
        {
            MdAssociation.Register();
            return;
        }
        if (args.Length > 0 && string.Equals(args[0], "/unregister", StringComparison.OrdinalIgnoreCase))
        {
            MdAssociation.Unregister();
            return;
        }

        Application.EnableVisualStyles();
        Application.SetCompatibleTextRenderingDefault(false);

        if (args.Length == 0)
        {
            MdAssociation.Register(); // first run: auto-register, then explain
            Mutex hostMutex;
            if (!TryAcquireHostMutex(out hostMutex))
            {
                // Already running (likely resident in the tray): just show the
                // welcome note through the existing host for a single source
                // of truth about registration state.
                ForwardWelcome();
                return;
            }
            Application.Run(new HostContext(hostMutex, null));
            return;
        }

        string path = args[0].Trim().Trim('"');
        if (string.IsNullOrEmpty(path) || !File.Exists(path))
        {
            return; // Missing file: stay silent, like the retired md-open.pyw.
        }

        // Single-instance dance: a live host wins; forward and leave. A dead
        // host releases the mutex (abandoned) and we take over. A wedged host
        // (pipe timeout, mutex held) degrades to the old standalone window.
        bool created;
        Mutex mutex = new Mutex(true, MutexName, out created);
        if (!created)
        {
            GrantForegroundToHost();
            if (ForwardToHost(path))
            {
                return;
            }
            Thread.Sleep(300); // give a busy host a moment, then retry
            if (ForwardToHost(path))
            {
                return;
            }
            try
            {
                if (mutex.WaitOne(0)) // abandoned => host died mid-flight
                {
                    created = true;
                }
            }
            catch (AbandonedMutexException)
            {
                created = true; // we own it now
            }
            if (!created)
            {
                // Extremely rare (host alive but pipe broken): open the file
                // standalone exactly like the pre-single-instance build.
                Application.Run(new ViewerForm(path, null));
                return;
            }
        }

        if (!MdAssociation.IsRegistered())
        {
            MdAssociation.Register(); // self-heal, but skip the registry write
        }                              // (and shell broadcast) on every launch
        Application.Run(new HostContext(mutex, path));
    }

    private static bool TryAcquireHostMutex(out Mutex mutex)
    {
        bool created;
        mutex = new Mutex(true, MutexName, out created);
        return created;
    }

    /// <summary>
    /// Let the resident host bring its window to the foreground: the shell
    /// grants that right to US (we were just launched), and we pass it on.
    /// Best-effort; ignored when no host is running.
    /// </summary>
    private static void GrantForegroundToHost()
    {
        try
        {
            string pidFile = Path.Combine(UserDataFolder, "host.pid");
            if (File.Exists(pidFile))
            {
                int pid = int.Parse(File.ReadAllText(pidFile).Trim());
                if (pid != Process.GetCurrentProcess().Id && MayRun(pid))
                {
                    AllowSetForegroundWindow(pid);
                }
            }
        }
        catch
        {
            // Cosmetic only: without the grant the window still shows, the
            // user may need one extra click to focus it.
        }
    }

    private static bool MayRun(int pid)
    {
        try
        {
            Process p = Process.GetProcessById(pid);
            return p != null && !p.HasExited;
        }
        catch
        {
            return false;
        }
    }

    /// <summary>Send the file path to the resident host; true on delivery.</summary>
    private static bool ForwardToHost(string path)
    {
        try
        {
            using (NamedPipeClientStream client =
                new NamedPipeClientStream(".", PipeName, PipeDirection.Out))
            {
                client.Connect(1500);
                byte[] data = Encoding.UTF8.GetBytes(path);
                client.Write(BitConverter.GetBytes(data.Length), 0, 4);
                client.Write(data, 0, data.Length);
                client.Flush();
                return true;
            }
        }
        catch
        {
            return false;
        }
    }

    /// <summary>Ask the resident host to reopen the welcome note.</summary>
    private static bool ForwardWelcome()
    {
        try
        {
            using (NamedPipeClientStream client =
                new NamedPipeClientStream(".", PipeName, PipeDirection.Out))
            {
                client.Connect(1000);
                byte[] data = Encoding.UTF8.GetBytes("\u0001welcome");
                client.Write(BitConverter.GetBytes(data.Length), 0, 4);
                client.Write(data, 0, data.Length);
                client.Flush();
                return true;
            }
        }
        catch
        {
            return false;
        }
    }

    [DllImport("user32.dll")]
    private static extern bool AllowSetForegroundWindow(int dwProcessId);

    /// <summary>
    /// Load Microsoft.Web.WebView2.* from embedded resources (no DLL files
    /// next to the exe).
    /// </summary>
    private static Assembly OnAssemblyResolve(object sender, ResolveEventArgs args)
    {
        string simpleName = new AssemblyName(args.Name).Name;
        using (Stream s = Assembly.GetExecutingAssembly()
            .GetManifestResourceStream("MDViewer.res." + simpleName + ".dll"))
        {
            if (s == null)
            {
                return null;
            }
            byte[] data = new byte[s.Length];
            s.Read(data, 0, data.Length);
            return Assembly.Load(data);
        }
    }

    /// <summary>
    /// The native WebView2Loader.dll cannot load from a resource stream:
    /// drop it into the user-data folder under %LOCALAPPDATA% (writable
    /// regardless of where the exe itself lives) and put that directory on
    /// the DLL search path.
    /// </summary>
    private static void EnsureLoaderDll()
    {
        try
        {
            string dst = Path.Combine(UserDataFolder, "WebView2Loader.dll");
            if (!File.Exists(dst))
            {
                Directory.CreateDirectory(UserDataFolder);
                using (Stream s = Assembly.GetExecutingAssembly()
                    .GetManifestResourceStream("MDViewer.res.WebView2Loader.dll"))
                using (FileStream fs = File.Create(dst))
                {
                    CopyStream(s, fs);
                }
            }
            SetDllDirectory(UserDataFolder);
        }
        catch
        {
            // A failed loader drop surfaces later as the WebView2-missing
            // fallback; nothing sensible to do this early.
        }
    }

    private static void CopyStream(Stream src, Stream dst)
    {
        byte[] buf = new byte[8192];
        int n;
        while ((n = src.Read(buf, 0, buf.Length)) > 0)
        {
            dst.Write(buf, 0, n);
        }
    }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetDllDirectory(string path);
}

/// <summary>
/// User settings persisted as plain ini under %LOCALAPPDATA%\Jianxiang\MdViewer.
/// keepRunning=0 (default): closing the last window exits the host.
/// keepRunning=1: the host stays resident (tray icon + hidden keep-alive
/// WebView) so the next document opens instantly.
/// </summary>
internal static class Settings
{
    private static string FilePath
    {
        get { return Path.Combine(MDViewerProgram.UserDataFolder, "settings.ini"); }
    }

    public static bool KeepRunning = Load();

    private static bool Load()
    {
        try
        {
            foreach (string raw in File.ReadAllLines(FilePath))
            {
                string line = raw.Trim();
                if (line.StartsWith("keepRunning=", StringComparison.OrdinalIgnoreCase))
                {
                    string v = line.Substring(12).Trim();
                    return v == "1" || string.Equals(v, "true", StringComparison.OrdinalIgnoreCase);
                }
            }
        }
        catch
        {
            // Missing/corrupt file simply means defaults.
        }
        return false;
    }

    public static void Save()
    {
        try
        {
            Directory.CreateDirectory(MDViewerProgram.UserDataFolder);
            File.WriteAllText(FilePath,
                "keepRunning=" + (KeepRunning ? "1" : "0") + "\r\n",
                new UTF8Encoding(false));
        }
        catch
        {
            // Read-only media etc.: the setting just won't persist.
        }
    }
}

/// <summary>
/// HKCU .md association (ProgId MarkdownViewer.md). Register keeps the old
/// default value inside the ProgId key (BackupDefault) so Unregister can
/// restore it without any external file.
/// </summary>
internal static class MdAssociation
{
    private const string ProgId = "MarkdownViewer.md";
    private const string ClassesRoot = @"Software\Classes";
    private const string UserChoiceKey =
        @"Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\.md\UserChoice";
    private static readonly string ExePath = Assembly.GetExecutingAssembly().Location;

    public static void Register()
    {
        try
        {
            using (RegistryKey progIdKey = Registry.CurrentUser
                .CreateSubKey(ClassesRoot + "\\" + ProgId))
            {
                progIdKey.SetValue(null, MDViewerProgram.AppName);
                using (RegistryKey iconKey = progIdKey.CreateSubKey("DefaultIcon"))
                {
                    iconKey.SetValue(null, "\"" + ExePath + "\",0");
                }
                using (RegistryKey cmdKey = progIdKey.CreateSubKey(@"shell\open\command"))
                {
                    cmdKey.SetValue(null, "\"" + ExePath + "\" \"%1\"");
                }
            }

            using (RegistryKey openWith = Registry.CurrentUser
                .CreateSubKey(ClassesRoot + @"\.md\OpenWithProgids"))
            {
                openWith.SetValue(ProgId, new byte[0], RegistryValueKind.None);
            }

            // Point the .md default at our ProgId, backing up whatever was
            // there first (install state lives in the registry, no files).
            using (RegistryKey mdKey = Registry.CurrentUser.CreateSubKey(ClassesRoot + "\\.md"))
            {
                string current = mdKey.GetValue(null) as string;
                if (current != ProgId)
                {
                    using (RegistryKey progIdKey = Registry.CurrentUser
                        .CreateSubKey(ClassesRoot + "\\" + ProgId))
                    {
                        if (progIdKey.GetValue("BackupDefault") == null
                            && !string.IsNullOrEmpty(current))
                        {
                            progIdKey.SetValue("BackupDefault", current);
                        }
                    }
                    mdKey.SetValue(null, ProgId);
                }
            }

            // A UserChoice captured by another app shadows the default above;
            // removing it makes Windows ask again with MD查看器 in the list.
            RemoveUserChoiceIfNotOurs();
            ShellChangeNotify();
        }
        catch
        {
            // Association registration needs no elevation under HKCU; failure
            // (policy-locked machines) degrades to manual "open with" choice.
        }
    }

    public static void Unregister()
    {
        try
        {
            string backup = null;
            using (RegistryKey progIdKey = Registry.CurrentUser
                .OpenSubKey(ClassesRoot + "\\" + ProgId))
            {
                if (progIdKey != null)
                {
                    backup = progIdKey.GetValue("BackupDefault") as string;
                }
            }

            using (RegistryKey mdKey = Registry.CurrentUser
                .OpenSubKey(ClassesRoot + "\\.md", true))
            {
                if (mdKey != null && (mdKey.GetValue(null) as string) == ProgId)
                {
                    mdKey.SetValue(null, string.IsNullOrEmpty(backup) ? "md_auto_file" : backup);
                }
            }

            using (RegistryKey openWith = Registry.CurrentUser
                .OpenSubKey(ClassesRoot + @"\.md\OpenWithProgids", true))
            {
                if (openWith != null)
                {
                    openWith.DeleteValue(ProgId, false);
                }
            }

            // Drop a remembered default that points at us so Windows stops
            // calling the (about-to-be-deleted) exe.
            string remembered = null;
            using (RegistryKey uc = Registry.CurrentUser.OpenSubKey(UserChoiceKey))
            {
                if (uc != null)
                {
                    remembered = uc.GetValue("ProgId") as string;
                }
            }
            if (remembered == ProgId)
            {
                Registry.CurrentUser.DeleteSubKeyTree(UserChoiceKey, false);
            }

            Registry.CurrentUser.DeleteSubKeyTree(ClassesRoot + "\\" + ProgId, false);
            ShellChangeNotify();
        }
        catch
        {
            // Best-effort cleanup during uninstall; leftovers are inert
            // registry values that re-running /register overwrites anyway.
        }
    }

    public static bool IsRegistered()
    {
        using (RegistryKey cmdKey = Registry.CurrentUser
            .OpenSubKey(ClassesRoot + "\\" + ProgId + @"\shell\open\command"))
        {
            string cmd = cmdKey != null ? cmdKey.GetValue(null) as string : null;
            return cmd != null
                && cmd.IndexOf(ExePath, StringComparison.OrdinalIgnoreCase) >= 0;
        }
    }

    private static void RemoveUserChoiceIfNotOurs()
    {
        string remembered = null;
        using (RegistryKey uc = Registry.CurrentUser.OpenSubKey(UserChoiceKey))
        {
            if (uc == null)
            {
                return;
            }
            remembered = uc.GetValue("ProgId") as string;
        }
        if (!string.IsNullOrEmpty(remembered) && remembered != ProgId)
        {
            Registry.CurrentUser.DeleteSubKeyTree(UserChoiceKey, false);
        }
    }

    [DllImport("shell32.dll")]
    private static extern void SHChangeNotify(int wEventId, uint uFlags, IntPtr dwItem1, IntPtr dwItem2);

    private const int SHCNE_ASSOCCHANGED = 0x08000000;
    private const uint SHCNF_IDLIST = 0;

    private static void ShellChangeNotify()
    {
        SHChangeNotify(SHCNE_ASSOCCHANGED, SHCNF_IDLIST, IntPtr.Zero, IntPtr.Zero);
    }
}

/// <summary>
/// Owns the process lifetime: shared WebView2 environment, the named-pipe
/// listener, window bookkeeping, and the optional resident (tray) mode.
/// Application.Run(ctx) keeps pumping messages even with zero windows while
/// resident, so the pipe listener can wake us for the next file.
/// </summary>
internal sealed class HostContext : ApplicationContext
{
    private readonly Mutex hostMutex;        // kept alive for the process lifetime;
                                             // GC of the mutex object would release
                                             // the kernel handle and break the
                                             // single-instance guarantee
    private readonly SynchronizationContext ui;
    private readonly object envGate = new object();
    private CoreWebView2Environment env;      // shared across all windows
    private Task<CoreWebView2Environment> envTask;
    private readonly List<Form> windows = new List<Form>();
    private NamedPipeServerStream listener;   // current acceptor (rebuilt per request)
    private NotifyIcon tray;
    private MenuItem keepMenuItem;
    private Form keepAlive;                   // hidden WebView keeping the browser alive
    private bool exiting;

    public HostContext(Mutex hostMutex, string initialFile)
    {
        this.hostMutex = hostMutex;
        // UI-thread marshalling for pipe callbacks (any thread -> UI).
        ui = new WindowsFormsSynchronizationContext();
        SynchronizationContext.SetSynchronizationContext(ui);

        try
        {
            Directory.CreateDirectory(MDViewerProgram.UserDataFolder);
            File.WriteAllText(
                Path.Combine(MDViewerProgram.UserDataFolder, "host.pid"),
                Process.GetCurrentProcess().Id.ToString());
        }
        catch
        {
            // Without the pid file the foreground grant degrades gracefully.
        }

        StartPipeListener();

        if (!string.IsNullOrEmpty(initialFile))
        {
            OpenFile(initialFile);
        }
        else
        {
            OpenWelcome();
        }
    }

    // ---------------------------------------------------------------- env --

    /// <summary>
    /// One WebView2 environment per process: every window (viewer, welcome,
    /// keep-alive) attaches to the same browser process tree, so opening the
    /// second+ document never respawns msedgewebview2.
    /// </summary>
    public async Task<CoreWebView2Environment> GetEnvironmentAsync()
    {
        while (true)
        {
            Task<CoreWebView2Environment> task;
            lock (envGate)
            {
                if (env != null)
                {
                    return env;
                }
                if (envTask == null)
                {
                    envTask = CoreWebView2Environment.CreateAsync(
                        null, MDViewerProgram.UserDataFolder, null);
                }
                task = envTask;
            }
            try
            {
                env = await task.ConfigureAwait(false);
                return env;
            }
            catch
            {
                lock (envGate)
                {
                    if (ReferenceEquals(envTask, task))
                    {
                        envTask = null; // allow a retry on the next window
                    }
                }
                throw;
            }
        }
    }

    // -------------------------------------------------------------- files --

    /// <summary>Open a document window; safe to call from any thread.</summary>
    public void OpenFile(string path)
    {
        ui.Post(delegate
        {
            if (exiting)
            {
                return;
            }
            DiscardKeepAlive();
            ViewerForm form = new ViewerForm(path, this);
            Track(form);
            form.Show();
            FocusWindow(form);
        }, null);
    }

    /// <summary>Open the welcome/settings window; safe from any thread.</summary>
    public void OpenWelcome()
    {
        ui.Post(delegate
        {
            if (exiting)
            {
                return;
            }
            DiscardKeepAlive();
            foreach (Form f in windows.ToArray())
            {
                WelcomeForm wf = f as WelcomeForm;
                if (wf != null)
                {
                    f.Activate();
                    return;
                }
            }
            WelcomeForm form = new WelcomeForm(this);
            Track(form);
            form.Show();
            FocusWindow(form);
        }, null);
    }

    private static void FocusWindow(Form form)
    {
        try
        {
            if (!form.IsHandleCreated)
            {
                IntPtr h = form.Handle; // force handle creation (CreateControl
            }                            // skips invisible controls)
            SetForegroundWindow(form.Handle);
        }
        catch
        {
            // Foreground grants can be denied; the window is still visible.
        }
        form.Activate();
    }

    private void Track(Form form)
    {
        windows.Add(form);
        form.FormClosed += delegate
        {
            windows.Remove(form);
            if (windows.Count == 0)
            {
                AllWindowsClosed();
            }
        };
    }

    private void AllWindowsClosed()
    {
        if (exiting)
        {
            return;
        }
        if (Settings.KeepRunning)
        {
            EnterIdle();
        }
        else
        {
            Shutdown();
        }
    }

    // -------------------------------------------------------------- idle --

    /// <summary>
    /// Resident mode: tray icon + hidden keep-alive WebView so the browser
    /// process tree survives; the next document opens with no cold start.
    /// </summary>
    private void EnterIdle()
    {
        ShowTray(true);
        BeginKeepAlive();
        try
        {
            // Trim our own working set; the browser processes manage theirs.
            using (Process p = Process.GetCurrentProcess())
            {
                UIntPtr trim = new UIntPtr(ulong.MaxValue); // (UIntPtr)(-1)
                SetProcessWorkingSetSize(p.Handle, trim, trim);
            }
        }
        catch
        {
            // Cosmetic memory trim only.
        }
    }

    private async void BeginKeepAlive()
    {
        try
        {
            Form form = new Form();
            form.FormBorderStyle = FormBorderStyle.None;
            form.ShowInTaskbar = false;
            form.WindowState = FormWindowState.Minimized;
            form.Opacity = 0;
            form.Size = new Size(1, 1);
            form.StartPosition = FormStartPosition.Manual;
            form.Location = new Point(-32000, -32000);

            WebView2 wv = new WebView2();
            wv.Dock = DockStyle.Fill;
            form.Controls.Add(wv);

            keepAlive = form;
            form.Show();   // creates the handle; minimized + opacity 0 => invisible
            form.Hide();
            await wv.EnsureCoreWebView2Async(await GetEnvironmentAsync());
        }
        catch
        {
            // Without WebView2 the resident still opens documents, just with
            // the usual cold start (and falls back to the browser if broken).
            DiscardKeepAlive();
        }
    }

    private void DiscardKeepAlive()
    {
        if (keepAlive != null)
        {
            Form f = keepAlive;
            keepAlive = null;
            try { f.Close(); } catch { }
        }
    }

    private void ShowTray(bool visible)
    {
        if (visible)
        {
            if (tray != null)
            {
                tray.Visible = true;
                return;
            }
            try
            {
                Icon icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath);
                ContextMenu menu = new ContextMenu();
                keepMenuItem = new MenuItem("保持后台待命（下次打开更快）",
                    delegate { ToggleKeepRunning(); });
                keepMenuItem.Checked = Settings.KeepRunning;
                menu.MenuItems.Add(keepMenuItem);
                menu.MenuItems.Add(new MenuItem("打开说明", delegate { OpenWelcome(); }));
                menu.MenuItems.Add(new MenuItem("-"));
                menu.MenuItems.Add(new MenuItem("退出", delegate { Shutdown(); }));
                tray = new NotifyIcon();
                tray.Icon = icon;
                tray.ContextMenu = menu;
                tray.Text = "MD查看器 · 后台待命";
                tray.Visible = true;
                tray.DoubleClick += delegate { OpenWelcome(); };
            }
            catch
            {
                // Tray creation failure is non-fatal; resident mode just has
                // no visible affordance (settings still work from windows).
            }
        }
        else if (tray != null)
        {
            tray.Visible = false;
        }
    }

    private void ToggleKeepRunning()
    {
        Settings.KeepRunning = !Settings.KeepRunning;
        Settings.Save();
        if (keepMenuItem != null)
        {
            keepMenuItem.Checked = Settings.KeepRunning;
        }
        if (!Settings.KeepRunning && windows.Count == 0)
        {
            // User turned residency off while resident: honor it now.
            Shutdown();
        }
    }

    /// <summary>Propagate a settings change made inside a web page.</summary>
    public void NotifyKeepChanged()
    {
        if (keepMenuItem != null)
        {
            ui.Post(delegate { keepMenuItem.Checked = Settings.KeepRunning; }, null);
        }
    }

    // -------------------------------------------------------------- pipe --

    private void StartPipeListener()
    {
        Thread t = new Thread(PipeLoop);
        t.IsBackground = true;
        t.Start();
    }

    /// <summary>
    /// Length-prefixed UTF-8 paths; "\u0001welcome" asks for the welcome
    /// window. One connection at a time is plenty (shell launches are rare).
    /// </summary>
    private void PipeLoop()
    {
        while (!exiting)
        {
            NamedPipeServerStream server = null;
            try
            {
                server = new NamedPipeServerStream(
                    MDViewerProgram.PipeName, PipeDirection.In, 1,
                    PipeTransmissionMode.Byte, PipeOptions.Asynchronous);
                listener = server;
                server.WaitForConnection();
                string message = ReadMessage(server);
                if (message == "\u0001welcome")
                {
                    OpenWelcome();
                }
                else if (!string.IsNullOrEmpty(message) && File.Exists(message))
                {
                    OpenFile(message);
                }
            }
            catch
            {
                // Listener disposed during shutdown, or a client glitch;
                // either way loop around (exiting stops us outside).
            }
            finally
            {
                if (server != null)
                {
                    listener = null;
                    try { server.Dispose(); } catch { }
                }
            }
        }
    }

    private static string ReadMessage(PipeStream pipe)
    {
        byte[] lenBuf = new byte[4];
        if (!ReadExactly(pipe, lenBuf))
        {
            return null;
        }
        int len = BitConverter.ToInt32(lenBuf, 0);
        if (len <= 0 || len > 32768)
        {
            return null;
        }
        byte[] buf = new byte[len];
        if (!ReadExactly(pipe, buf))
        {
            return null;
        }
        return Encoding.UTF8.GetString(buf);
    }

    private static bool ReadExactly(PipeStream pipe, byte[] buffer)
    {
        int off = 0;
        while (off < buffer.Length)
        {
            int n = pipe.Read(buffer, off, buffer.Length - off);
            if (n <= 0)
            {
                return false;
            }
            off += n;
        }
        return true;
    }

    // ----------------------------------------------------------- shutdown --

    /// <summary>Stop the listener, drop the tray, and end the process.</summary>
    public void Shutdown()
    {
        ui.Post(delegate
        {
            if (exiting)
            {
                return;
            }
            exiting = true;
            try { if (listener != null) listener.Dispose(); } catch { }
            DiscardKeepAlive();
            if (tray != null)
            {
                try { tray.Visible = false; tray.Dispose(); } catch { }
                tray = null;
            }
            try { File.Delete(Path.Combine(MDViewerProgram.UserDataFolder, "host.pid")); }
            catch { }
            ExitThread();
        }, null);
    }

    [DllImport("user32.dll")]
    private static extern bool SetForegroundWindow(IntPtr hWnd);

    [DllImport("kernel32.dll")]
    private static extern bool SetProcessWorkingSetSize(IntPtr hProcess, UIntPtr min, UIntPtr max);
}

internal sealed class ViewerForm : Form
{
    private readonly string mdPath;
    private readonly HostContext host;     // null in the rare standalone mode
    private string tempHtmlPath;           // baked page; null when baking failed
    private string bakeError;              // user-facing reason when baking failed
    private readonly WebView2 webView;

    public ViewerForm(string mdPath, HostContext host)
    {
        this.mdPath = mdPath;
        this.host = host;

        string name = Path.GetFileName(mdPath);
        this.Text = name + " - " + MDViewerProgram.AppName;
        this.StartPosition = FormStartPosition.CenterScreen;
        this.Size = new Size(1080, 800);
        this.MinimumSize = new Size(480, 360);
        this.KeyPreview = true; // Esc closes the viewer window

        try
        {
            Icon icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath);
            if (icon != null)
            {
                this.Icon = icon;
            }
        }
        catch
        {
            // Missing icon is cosmetic only.
        }

        webView = new WebView2();
        webView.Dock = DockStyle.Fill;
        webView.CreationProperties = new CoreWebView2CreationProperties
        {
            UserDataFolder = MDViewerProgram.UserDataFolder
        };
        this.Controls.Add(webView);
    }

    protected override void OnShown(EventArgs e)
    {
        base.OnShown(e);
        InitWebView();
    }

    protected override bool ProcessCmdKey(ref Message msg, Keys keyData)
    {
        if (keyData == Keys.Escape)
        {
            this.Close();
            return true;
        }
        return base.ProcessCmdKey(ref msg, keyData);
    }

    private async void InitWebView()
    {
        // Bake off the UI thread so even multi-megabyte files never delay the
        // window appearing.
        string baked = null; string error = null;
        await Task.Run(delegate { BakeHtml(mdPath, out baked, out error); });
        this.tempHtmlPath = baked;
        this.bakeError = error;

        if (this.tempHtmlPath == null)
        {
            MessageBox.Show(
                "无法生成预览页面：" + this.bakeError,
                MDViewerProgram.AppName,
                MessageBoxButtons.OK,
                MessageBoxIcon.Error);
            this.Close();
            return;
        }

        try
        {
            string version = CoreWebView2Environment.GetAvailableBrowserVersionString();
            if (string.IsNullOrEmpty(version))
            {
                FallbackToDefaultBrowser();
                return;
            }

            // Attach to the host's shared environment (same browser process)
            // so this window opens without a cold start.
            CoreWebView2Environment environment = host != null
                ? await host.GetEnvironmentAsync()
                : await CoreWebView2Environment.CreateAsync(
                    null, MDViewerProgram.UserDataFolder, null);
            await webView.EnsureCoreWebView2Async(environment);
            webView.CoreWebView2.WebMessageReceived += OnWebMessage;
            webView.CoreWebView2.Navigate(new Uri(this.tempHtmlPath).AbsoluteUri);
        }
        catch (Exception)
        {
            // Runtime present but init failed (corrupt install etc.) — the
            // baked page is still a valid HTML file, hand it to the browser.
            FallbackToDefaultBrowser();
        }
    }

    private void OnWebMessage(object sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        string msg;
        try
        {
            msg = e.TryGetWebMessageAsString();
        }
        catch (Exception)
        {
            return;
        }
        if (msg == "keep:on" || msg == "keep:off")
        {
            Settings.KeepRunning = msg == "keep:on";
            Settings.Save();
            if (host != null)
            {
                host.NotifyKeepChanged();
            }
        }
    }

    private void FallbackToDefaultBrowser()
    {
        MessageBox.Show(
            "未检测到可用的 WebView2 运行时。\n\n将改用系统默认浏览器打开本文档。\n建议安装 Microsoft Edge WebView2 Runtime 以获得最佳体验。",
            MDViewerProgram.AppName,
            MessageBoxButtons.OK,
            MessageBoxIcon.Information);
        try
        {
            Process.Start(new ProcessStartInfo
            {
                FileName = this.tempHtmlPath,
                UseShellExecute = true
            });
        }
        catch
        {
            // Even the browser hand-off failed; the error box above already
            // told the user what is wrong.
        }
        this.Close();
    }

    /// <summary>
    /// Bake the markdown file into the embedded viewer template (same
    /// placeholders as the retired md-open.pyw) and return the baked page's
    /// path (null on failure with the reason in <paramref name="error"/>).
    /// </summary>
    private static void BakeHtml(string path, out string result, out string error)
    {
        result = null;
        error = null;
        try
        {
            // Encoding.UTF8 decoding replaces invalid byte sequences with
            // U+FFFD, matching Python's errors="replace".
            string tpl = LoadTemplate();
            string md = File.ReadAllText(path, Encoding.UTF8);
            string name = Path.GetFileName(path);

            string b64 = Convert.ToBase64String(Encoding.UTF8.GetBytes(md));
            string meta = name + " · " + md.Length + " 字符";

            string html = tpl
                .Replace("__TITLE__", EscapeHtml(name))
                .Replace("__META__", EscapeHtml(meta))
                .Replace("__KEEP_RUNNING__", Settings.KeepRunning ? "true" : "false")
                .Replace("__B64__", b64);

            string outDir = Path.Combine(Path.GetTempPath(), "mdviewer");
            Directory.CreateDirectory(outDir);
            string outPath = Path.Combine(outDir, name + ".html");
            File.WriteAllText(outPath, html, new UTF8Encoding(false));
            result = outPath;
        }
        catch (Exception ex)
        {
            error = ex.Message;
        }
    }

    private static string LoadTemplate()
    {
        using (Stream s = Assembly.GetExecutingAssembly()
            .GetManifestResourceStream("MDViewer.res.viewer-template.html"))
        {
            if (s == null)
            {
                throw new IOException("查看器模板资源缺失。");
            }
            using (StreamReader r = new StreamReader(s, Encoding.UTF8))
            {
                return r.ReadToEnd();
            }
        }
    }

    private static string EscapeHtml(string s)
    {
        return s
            .Replace("&", "&amp;")
            .Replace("<", "&lt;")
            .Replace(">", "&gt;")
            .Replace("\"", "&quot;");
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && webView != null)
        {
            webView.Dispose();
        }
        base.Dispose(disposing);
    }
}

/// <summary>
/// Shown when the exe runs without arguments: registration already happened.
/// A modern WebUI welcome page (embedded welcome.html in a WebView2 window)
/// explains how to pick MD查看器 in the Windows "how do you want to open this
/// file" dialog. Buttons talk back via window.chrome.webview.postMessage.
/// </summary>
internal sealed class WelcomeForm : Form
{
    private readonly HostContext host;
    private readonly WebView2 webView;

    public WelcomeForm(HostContext host)
    {
        this.host = host;
        this.Text = MDViewerProgram.AppName;
        this.StartPosition = FormStartPosition.CenterScreen;
        this.FormBorderStyle = FormBorderStyle.FixedSingle;
        this.MaximizeBox = false;
        this.ClientSize = new Size(680, 780);

        try
        {
            Icon icon = Icon.ExtractAssociatedIcon(Application.ExecutablePath);
            if (icon != null)
            {
                this.Icon = icon;
            }
        }
        catch
        {
            // Missing icon is cosmetic only.
        }

        webView = new WebView2();
        webView.Dock = DockStyle.Fill;
        webView.CreationProperties = new CoreWebView2CreationProperties
        {
            UserDataFolder = MDViewerProgram.UserDataFolder
        };
        this.Controls.Add(webView);
    }

    protected override void OnShown(EventArgs e)
    {
        base.OnShown(e);
        InitWebView();
    }

    private async void InitWebView()
    {
        try
        {
            string version = CoreWebView2Environment.GetAvailableBrowserVersionString();
            if (string.IsNullOrEmpty(version))
            {
                // The welcome page is advisory; without WebView2 just confirm
                // registration in a plain message box.
                MessageBox.Show(
                    "MD查看器 已注册。\n\n双击 .md 文件，在「你要如何打开这个文件？」中选择 MD查看器\n并勾选「始终使用此应用」。",
                    MDViewerProgram.AppName,
                    MessageBoxButtons.OK,
                    MessageBoxIcon.Information);
                this.Close();
                return;
            }

            CoreWebView2Environment environment = host != null
                ? await host.GetEnvironmentAsync()
                : await CoreWebView2Environment.CreateAsync(
                    null, MDViewerProgram.UserDataFolder, null);
            await webView.EnsureCoreWebView2Async(environment);
            webView.CoreWebView2.WebMessageReceived += OnWebMessage;
            Navigate();
        }
        catch
        {
            // Init failure leaves nothing to show; registration already ran.
            this.Close();
        }
    }

    private void OnWebMessage(object sender, CoreWebView2WebMessageReceivedEventArgs e)
    {
        string msg;
        try
        {
            msg = e.TryGetWebMessageAsString();
        }
        catch (Exception)
        {
            // Non-string messages cannot come from the embedded page.
            return;
        }
        if (msg == "close")
        {
            this.Close();
        }
        else if (msg == "unregister")
        {
            MdAssociation.Unregister();
            Navigate(); // rebuild the page so the badge shows the new state
        }
        else if (msg == "keep:on" || msg == "keep:off")
        {
            Settings.KeepRunning = msg == "keep:on";
            Settings.Save();
            if (host != null)
            {
                host.NotifyKeepChanged();
            }
        }
    }

    private void Navigate()
    {
        string outDir = Path.Combine(Path.GetTempPath(), "mdviewer");
        Directory.CreateDirectory(outDir);
        string outPath = Path.Combine(outDir, "welcome.html");
        File.WriteAllText(outPath, LoadWelcomeHtml(), new UTF8Encoding(false));
        webView.CoreWebView2.Navigate(new Uri(outPath).AbsoluteUri);
    }

    private static string LoadWelcomeHtml()
    {
        using (Stream s = Assembly.GetExecutingAssembly()
            .GetManifestResourceStream("MDViewer.res.welcome.html"))
        {
            if (s == null)
            {
                throw new IOException("欢迎页资源缺失。");
            }
            using (StreamReader r = new StreamReader(s, Encoding.UTF8))
            {
                string tpl = r.ReadToEnd();
                bool ok = MdAssociation.IsRegistered();
                return tpl
                    .Replace("__STATUS__", ok
                        ? "√ 已注册为 .md 打开方式"
                        : "× 未注册（双击 .md 时请在系统询问框中选择 MD查看器）")
                    .Replace("__STATUS_CLASS__", ok ? "ok" : "off")
                    .Replace("__KEEP_RUNNING__", Settings.KeepRunning ? "true" : "false");
            }
        }
    }

    protected override void Dispose(bool disposing)
    {
        if (disposing && webView != null)
        {
            webView.Dispose();
        }
        base.Dispose(disposing);
    }
}
