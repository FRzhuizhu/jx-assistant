from enum import IntFlag

import comtypes.gen._00020430_0000_0000_C000_000000000046_0_2_0 as __wrapper_module__
from comtypes.gen._00020430_0000_0000_C000_000000000046_0_2_0 import (
    IFontDisp, typelib_path, OLE_YPOS_CONTAINER, OLE_YSIZE_HIMETRIC,
    GUID, IPictureDisp, StdFont, OLE_CANCELBOOL, IEnumVARIANT,
    OLE_COLOR, FONTSIZE, _lcid, FONTBOLD, OLE_HANDLE, IPicture,
    IFontEventsDisp, Library, FontEvents, DISPPROPERTY, IUnknown,
    Unchecked, DISPMETHOD, Monochrome, StdPicture, IFont, Color,
    OLE_XSIZE_PIXELS, Gray, OLE_YSIZE_PIXELS, OLE_ENABLEDEFAULTBOOL,
    Font, Checked, OLE_YPOS_PIXELS, VARIANT_BOOL, FONTITALIC,
    OLE_YPOS_HIMETRIC, VgaColor, OLE_XPOS_HIMETRIC, DISPPARAMS,
    OLE_XSIZE_HIMETRIC, OLE_XPOS_PIXELS, _check_version, CoClass,
    IDispatch, OLE_YSIZE_CONTAINER, EXCEPINFO, FONTNAME, Picture,
    FONTUNDERSCORE, dispid, OLE_OPTEXCLUSIVE, HRESULT, Default,
    FONTSTRIKETHROUGH, BSTR, COMMETHOD, OLE_XSIZE_CONTAINER,
    OLE_XPOS_CONTAINER
)


class OLE_TRISTATE(IntFlag):
    Unchecked = 0
    Checked = 1
    Gray = 2


class LoadPictureConstants(IntFlag):
    Default = 0
    Monochrome = 1
    VgaColor = 2
    Color = 4


__all__ = [
    'OLE_YPOS_HIMETRIC', 'VgaColor', 'IFontDisp', 'OLE_XPOS_HIMETRIC',
    'typelib_path', 'OLE_YPOS_CONTAINER', 'OLE_YSIZE_HIMETRIC',
    'IPictureDisp', 'StdFont', 'OLE_CANCELBOOL', 'OLE_COLOR',
    'FONTSIZE', 'OLE_XSIZE_HIMETRIC', 'FONTBOLD', 'OLE_HANDLE',
    'IPicture', 'OLE_XPOS_PIXELS', 'IFontEventsDisp', 'Library',
    'FontEvents', 'Unchecked', 'Monochrome', 'OLE_YSIZE_CONTAINER',
    'LoadPictureConstants', 'StdPicture', 'IFont', 'Color',
    'FONTNAME', 'Picture', 'OLE_XSIZE_PIXELS', 'Gray',
    'FONTUNDERSCORE', 'OLE_YSIZE_PIXELS', 'OLE_OPTEXCLUSIVE',
    'OLE_TRISTATE', 'OLE_ENABLEDEFAULTBOOL', 'Font',
    'FONTSTRIKETHROUGH', 'Default', 'FONTITALIC', 'Checked',
    'OLE_XSIZE_CONTAINER', 'OLE_XPOS_CONTAINER', 'OLE_YPOS_PIXELS'
]

